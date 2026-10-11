package db

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

func decodeAssetJSON(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(target)
}

// Scoped reads and writes must use their owning immediate transaction. The
// independent reader cannot see its uncommitted asset/association side effects.
func (s *AssetStore) query(q string, args ...any) (*sql.Rows, error) {
	if s.tx != nil {
		return s.tx.Query(q, args...)
	}
	return s.db.Query(q, args...)
}
func (s *AssetStore) queryRow(q string, args ...any) *sql.Row {
	if s.tx != nil {
		return s.tx.QueryRow(q, args...)
	}
	return s.db.QueryRow(q, args...)
}
func (s *AssetStore) exec(q string, args ...any) (sql.Result, error) {
	if s.tx != nil {
		return s.tx.Exec(q, args...)
	}
	return s.db.Exec(q, args...)
}

// readPool is an explicit pure-read seam, including SELECT-only CTEs. DB.Query
// conservatively sends WITH to the writer because a CTE may mutate; callers
// with known read SQL must opt in instead of changing that safe classifier.
func (d *DB) readPool() *sql.DB {
	if d.IsSQLite() && d.reader != nil {
		return d.reader
	}
	return d.DB
}
func (s *AssetStore) readQuery(q string, args ...any) (*sql.Rows, error) {
	if s.tx != nil {
		return s.tx.Query(q, args...)
	}
	return s.db.readPool().Query(q, args...)
}
func (s *AssetStore) readQueryRow(q string, args ...any) *sql.Row {
	if s.tx != nil {
		return s.tx.QueryRow(q, args...)
	}
	return s.db.readPool().QueryRow(q, args...)
}

func (s *AssetStore) taskMembership(placeholder, column string) string {
	return s.db.SQL(placeholder+" = ANY("+column+")", "EXISTS(SELECT 1 FROM json_each("+column+") WHERE value="+placeholder+")")
}
func (s *AssetStore) pageClause(args *[]any, limit, offset int) string {
	if s.db.IsSQLite() && limit <= 0 && offset > 0 {
		*args = append(*args, offset)
		return fmt.Sprintf(" ORDER BY last_seen DESC,id DESC LIMIT -1 OFFSET $%d", len(*args))
	}
	return pageClause(args, limit, offset)
}

func (s *AssetStore) taskIDs(taskID int64) string {
	if s.db.IsSQLite() {
		if taskID > 0 {
			return fmt.Sprintf("[%d]", taskID)
		}
		return "[]"
	}
	if taskID > 0 {
		return fmt.Sprintf("{%d}", taskID)
	}
	return "{}"
}
func (s *AssetStore) stringArray(items []string) string {
	if !s.db.IsSQLite() {
		return marshalStringArray(items)
	}
	if items == nil {
		items = []string{}
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}
func (s *AssetStore) objectArray(items []map[string]any) (string, error) {
	if !s.db.IsSQLite() {
		return marshalJSONBArray(items)
	}
	if items == nil {
		items = []map[string]any{}
	}
	raw, err := json.Marshal(items)
	return string(raw), err
}
func (s *AssetStore) portArray(items []PortService) (string, error) {
	values := make([]map[string]any, len(items))
	for i, p := range items {
		values[i] = map[string]any{"port": p.Port, "service": p.Service}
	}
	return s.objectArray(values)
}

// PostgreSQL jsonb text orders object keys by byte length then byte value and
// includes separator spaces. Endpoint DISTINCT ON uses this text descending,
// so Go's ordinary lexicographic key ordering is not the same tie-break.
func pgJSONText(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pgJSONString(k) + ": " + pgJSONText(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, y := range x {
			parts[i] = pgJSONText(y)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case json.Number:
		return pgJSONNumberText(x)
	default:
		return pgJSONString(v)
	}
}

// jsonb numeric text expands exponents, removes integer leading zeroes and a
// zero's sign, while retaining fractional scale. Rational equality keys have a
// different purpose and must not replace this scale-preserving sorting text.
func pgJSONNumberText(number json.Number) string {
	text := string(number)
	negative := strings.HasPrefix(text, "-")
	mantissa := strings.TrimPrefix(text, "-")
	exponent := 0
	if e := strings.IndexAny(mantissa, "eE"); e >= 0 {
		var err error
		exponent, err = strconv.Atoi(mantissa[e+1:])
		if err != nil || exponent > 131072 || exponent < -16383 {
			return text
		}
		mantissa = mantissa[:e]
	}
	point := strings.IndexByte(mantissa, '.')
	if point < 0 {
		point = len(mantissa)
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	zero := strings.Trim(digits, "0") == ""
	point += exponent
	var expanded string
	if point <= 0 {
		expanded = "0." + strings.Repeat("0", -point) + digits
	} else if point >= len(digits) {
		expanded = digits + strings.Repeat("0", point-len(digits))
	} else {
		expanded = digits[:point] + "." + digits[point:]
	}
	parts := strings.SplitN(expanded, ".", 2)
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	result := integer
	if len(parts) == 2 {
		result += "." + parts[1]
	}
	if negative && !zero {
		result = "-" + result
	}
	return result
}

func pgJSONString(v any) string {
	value, ok := v.(string)
	if !ok {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// jsonb equality compares numbers by value, including nested numbers. Rational
// normalization distinguishes neither 1/1.0 nor 9007199254740993/... .0, while
// preserving every input digit in the selected object's serialized output.
func assetJSONSemanticKey(v any) string {
	switch x := v.(type) {
	case json.Number:
		if r, ok := new(big.Rat).SetString(string(x)); ok {
			return "n:" + r.RatString()
		}
		return "n:" + string(x)
	case []any:
		parts := make([]string, len(x))
		for i, value := range x {
			parts[i] = assetJSONSemanticKey(value)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			b, _ := json.Marshal(k)
			parts[i] = string(b) + ":" + assetJSONSemanticKey(x[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return pgJSONText(v)
	}
}

// PostgreSQL ->> returns nullable text, not a typed JSON identity: number 1
// and string "1" share a key; null/missing share SQL NULL. Arrays and objects
// use jsonb's canonical text. Encode the nullable pair to avoid ambiguity.
func pgJSONTextKey(value any) *string {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		text = pgJSONText(value)
	}
	return &text
}
func pgParamGroupKey(value any) string {
	var location, name any
	if m, ok := value.(map[string]any); ok {
		location = m["location"]
		name = m["name"]
	}
	raw, _ := json.Marshal([]*string{pgJSONTextKey(location), pgJSONTextKey(name)})
	return string(raw)
}

// Real JSON arrays in, real JSON arrays out. UseNumber avoids a float64 round
// trip for task IDs and nested user-supplied auth/params objects.
func sqliteAssetArrayMerge(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var all []any
	for _, arg := range args[:2] {
		var values []any
		if arg != nil {
			if err := decodeAssetJSON([]byte(fmt.Sprint(arg)), &values); err != nil {
				return nil, err
			}
		}
		all = append(all, values...)
	}
	kind := fmt.Sprint(args[2])
	out := []any{}
	byKey := map[string]int{}
	for _, v := range all {
		key := assetJSONSemanticKey(v)
		if kind == "params" {
			key = pgParamGroupKey(v)
		}
		if m, ok := v.(map[string]any); ok {
			switch kind {
			case "ports":
				key = fmt.Sprint(m["port"])
			}
		}
		if index, ok := byKey[key]; ok {
			switch kind {
			case "ports":
				old, oldOK := out[index].(map[string]any)
				next, nextOK := v.(map[string]any)
				if oldOK && nextOK && (old["service"] == nil || old["service"] == "") && next["service"] != nil && next["service"] != "" {
					out[index] = v
				}
			case "params":
				if pgJSONText(v) > pgJSONText(out[index]) {
					out[index] = v
				}
			}
			continue
		}
		byKey[key] = len(out)
		out = append(out, v)
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func sqliteNetContains(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil || args[1] == nil {
		return false, nil
	}
	network, err := netip.ParsePrefix(fmt.Sprint(args[0]))
	if err != nil {
		return false, nil
	}
	ip, err := netip.ParseAddr(fmt.Sprint(args[1]))
	if err != nil {
		return false, nil
	}
	return network.Addr().BitLen() == ip.BitLen() && network.Contains(ip), nil
}

func sqliteNormalizeICP(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if args[0] == nil {
		return "", nil
	}
	return NormalizeICP(fmt.Sprint(args[0])), nil
}

// Declared-scope membership only. Task3 coverage must add its fact/anchor union
// separately; silently mixing that union into this predicate changes list_assets.
const sqliteScopeTargetCTE = `
context_tasks AS (
 SELECT id AS task_id FROM tasks WHERE id=$1 AND deleted_at IS NULL
 UNION ALL
 SELECT source.id FROM task_relations relation
 JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
 WHERE relation.task_id=$1
), target AS (
 SELECT DISTINCT a.id FROM assets a
 JOIN task_scope ts ON (
      (ts.kind='company' AND a.company_id=ts.company_id)
   OR (ts.kind='root_domain' AND a.root_domain=ts.domain)
   OR (ts.kind='subdomain' AND a.domain=ts.domain)
   OR (ts.kind IN ('ip','cidr') AND (portable_net_contains(ts.net,a.ip) OR portable_net_contains(ts.net,a.domain)))
   OR (ts.kind='icp' AND (portable_normalize_icp(a.icp)=ts.value OR portable_normalize_icp(a.app_icp)=ts.value))
 ) JOIN context_tasks ctx ON ctx.task_id=ts.task_id
)`

func (s *AssetStore) scopeTargetSQL() string { return s.db.SQL(scopeTargetCTE, sqliteScopeTargetCTE) }
