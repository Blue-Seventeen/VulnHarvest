package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

func archiveColumnTypes(table string) (map[string]string, error) {
	spec, ok := archiveSQLiteColumns[table]
	if !ok {
		return nil, fmt.Errorf("archive table %q is not allowed", table)
	}
	out := map[string]string{}
	for _, field := range strings.Fields(spec) {
		pair := strings.SplitN(field, ":", 2)
		out[pair[0]] = pair[1]
	}
	return out, nil
}

// Read the same deferred snapshot, preserving JSON values, NULL and exact integers.
func sqliteArchiveRows(tx *sql.Tx, table, query string, output io.Writer, args ...any) (json.RawMessage, int64, error) {
	types, err := archiveColumnTypes(table)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, 0, err
	}
	var buffer strings.Builder
	if output == nil {
		buffer.WriteByte('[')
	}
	var count int64
	for rows.Next() {
		values, dest := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		row := map[string]any{}
		for i, col := range columns {
			typ, ok := types[col]
			if !ok {
				return nil, 0, fmt.Errorf("unlisted archive column %s.%s", table, col)
			}
			value := values[i]
			if value != nil {
				switch typ {
				case "JSON":
					raw := json.RawMessage(fmt.Sprint(value))
					if b, ok := value.([]byte); ok {
						raw = append(json.RawMessage(nil), b...)
					}
					if !json.Valid(raw) {
						return nil, 0, fmt.Errorf("invalid archive JSON %s.%s", table, col)
					}
					value = raw
				case "BOOLEAN":
					if n, ok := value.(int64); ok {
						value = n != 0
					}
				case "DATETIME":
					if stamp, ok := value.(time.Time); ok {
						value = stamp.UTC()
					}
				}
			}
			row[col] = value
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return nil, 0, err
		}
		if output != nil {
			raw = append(raw, '\n')
			if n, e := output.Write(raw); e != nil {
				return nil, 0, e
			} else if n != len(raw) {
				return nil, 0, io.ErrShortWrite
			}
		} else {
			if count > 0 {
				buffer.WriteByte(',')
			}
			buffer.Write(raw)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if output != nil {
		return nil, count, nil
	}
	buffer.WriteByte(']')
	return json.RawMessage(buffer.String()), count, nil
}

func sqliteArchiveValue(typ string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch typ {
	case "JSON":
		raw, err := json.Marshal(value)
		return string(raw), err
	case "INTEGER":
		if n, ok := value.(json.Number); ok {
			return n.Int64()
		}
	case "REAL":
		if n, ok := value.(json.Number); ok {
			return n.Float64()
		}
	case "DATETIME":
		if stamp, ok := value.(string); ok {
			v, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return nil, err
			}
			return v.UTC(), nil
		}
	}
	return value, nil
}

func sqliteArchiveInsert(table string, row map[string]any, conflict string) (string, []any, error) {
	types, err := archiveColumnTypes(table)
	if err != nil {
		return "", nil, err
	}
	columns := make([]string, 0, len(row))
	for col := range row {
		if _, ok := types[col]; !ok {
			return "", nil, fmt.Errorf("unlisted archive column %s.%s", table, col)
		}
		columns = append(columns, col)
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		return "", nil, fmt.Errorf("empty archive row %s", table)
	}
	values, parameters := make([]any, len(columns)), make([]string, len(columns))
	for i, col := range columns {
		values[i], err = sqliteArchiveValue(types[col], row[col])
		if err != nil {
			return "", nil, fmt.Errorf("%s.%s: %w", table, col, err)
		}
		parameters[i] = "$" + strconv.Itoa(i+1)
	}
	return "INSERT INTO " + table + "(" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(parameters, ",") + ")" + conflict, values, nil
}

func sqliteInsertArchiveRow(tx *sql.Tx, table string, row map[string]any, conflict string) (sql.Result, error) {
	query, values, err := sqliteArchiveInsert(table, row, conflict)
	if err != nil {
		return nil, err
	}
	return tx.Exec(query, values...)
}

func sqliteInsertArchiveRows(tx *sql.Tx, table string, raw json.RawMessage, conflict string) error {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err = sqliteInsertArchiveRow(tx, table, row, conflict); err != nil {
			return err
		}
	}
	return nil
}

func sqliteUpdateArchiveStub(tx *sql.Tx, table string, id int64, row map[string]any, columns []string) error {
	types, err := archiveColumnTypes(table)
	if err != nil {
		return err
	}
	values := []any{id}
	set := []string{}
	for _, col := range columns {
		value, exists := row[col]
		if !exists {
			continue
		}
		typ, ok := types[col]
		if !ok {
			return fmt.Errorf("unlisted stub column %s", col)
		}
		v, e := sqliteArchiveValue(typ, value)
		if e != nil {
			return e
		}
		values = append(values, v)
		set = append(set, col+"=$"+strconv.Itoa(len(values)))
	}
	_, err = tx.Exec("UPDATE "+table+" SET "+strings.Join(set, ",")+" WHERE id=$1", values...)
	return err
}

func (d *DB) archiveCols() string {
	if d.IsSQLite() {
		return strings.Replace(taskArchiveCols, "array_to_json(source_task_ids)::text", "source_task_ids", 1)
	}
	return taskArchiveCols
}
func (d *DB) archiveLockSuffix(s string) string {
	if d.IsSQLite() {
		return ""
	}
	return s
}
func (d *DB) archiveAssetIDsQuery() string {
	if !d.IsSQLite() {
		return archiveAssetIDsQuery()
	}
	return `SELECT id FROM assets WHERE EXISTS(SELECT 1 FROM json_each(assets.task_ids) WHERE value=$1)
 UNION SELECT asset_id FROM task_asset_links WHERE task_id=$1
 UNION SELECT a.asset_id FROM exploration_anchors a JOIN exploration_nodes n ON n.id=a.node_id WHERE n.exploration_id=$2
 UNION SELECT CAST(j.value AS INTEGER) FROM findings f JOIN json_each(f.asset_ids) j WHERE f.task_id=$1 AND j.type='integer'`
}
func (d *DB) archiveOtherAssetOwners() string {
	if d.IsSQLite() {
		return `EXISTS(SELECT 1 FROM json_each(asset.task_ids) j JOIN tasks t ON t.id=j.value WHERE t.id<>$1 AND t.deleted_at IS NULL)`
	}
	return `EXISTS(SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND task.id=ANY(asset.task_ids))`
}

func (d *DB) archiveSchemaLock(tx *sql.Tx) error {
	if d.IsSQLite() {
		return nil
	}
	return coordinateWithSchemaMigration(tx)
}
func (d *DB) archiveArray(value []int64) any {
	if !d.IsSQLite() {
		return value
	}
	if value == nil {
		value = []int64{}
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

// Summaries use only public counters and labels; raw bodies/configs never enter UI.
func sqliteArchiveAggregates(tables map[string]json.RawMessage) (map[string]any, error) {
	stats := map[string]any{}
	tokens := map[string]int64{"calls": 0, "input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}
	profiles := map[string]map[string]any{}
	daily := map[string]map[string]any{}
	usage, err := decodeArchiveRows(tables["llm_usage"])
	if err != nil {
		return nil, err
	}
	for _, row := range usage {
		name, _ := row["profile_name"].(string)
		p := profiles[name]
		if p == nil {
			p = map[string]any{"profile_name": name, "calls": int64(0), "tasks": 1, "input_tokens": int64(0), "output_tokens": int64(0), "cache_read_tokens": int64(0), "cache_write_tokens": int64(0)}
			profiles[name] = p
		}
		ts, _ := row["ts"].(string)
		stamp, e := time.Parse(time.RFC3339Nano, ts)
		if e != nil {
			return nil, e
		}
		date := stamp.UTC().Format("2006-01-02")
		key := name + "\x00" + date
		day := daily[key]
		if day == nil {
			day = map[string]any{"profile_name": name, "date": date, "input_tokens": int64(0), "output_tokens": int64(0), "cache_read_tokens": int64(0)}
			daily[key] = day
		}
		tokens["calls"]++
		p["calls"] = p["calls"].(int64) + 1
		for _, pair := range [][2]string{{"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"}, {"cache_read", "cache_read_tokens"}, {"cache_write", "cache_write_tokens"}} {
			n, _ := jsonInt64(row[pair[0]])
			tokens[pair[1]] += n
			p[pair[1]] = p[pair[1]].(int64) + n
			if pair[0] != "cache_write" {
				day[pair[1]] = day[pair[1]].(int64) + n
			}
		}
	}
	stats["tokens"] = tokens
	ps, ds := []map[string]any{}, []map[string]any{}
	for _, p := range profiles {
		ps = append(ps, p)
	}
	for _, d := range daily {
		ds = append(ds, d)
	}
	sort.Slice(ps, func(i, j int) bool { return fmt.Sprint(ps[i]["profile_name"]) < fmt.Sprint(ps[j]["profile_name"]) })
	sort.Slice(ds, func(i, j int) bool {
		return fmt.Sprint(ds[i]["date"])+fmt.Sprint(ds[i]["profile_name"]) < fmt.Sprint(ds[j]["date"])+fmt.Sprint(ds[j]["profile_name"])
	})
	stats["token_profiles"], stats["token_daily"] = ps, ds
	skills, tools, findings := map[string]int{}, map[string]int{}, map[string]int{}
	skillStats, missingStats := map[string]*SkillStat{}, map[string]*SkillStat{}
	rows, err := decodeArchiveRows(tables["skill_usage"])
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		name, _ := row["skill"].(string)
		found, _ := row["found"].(bool)
		set := missingStats
		if found {
			skills[name]++
			set = skillStats
		}
		s := set[name]
		if s == nil {
			s = &SkillStat{Skill: name, Agents: []string{}}
			if found {
				s.Tasks = 1
			}
			set[name] = s
		}
		s.Calls++
		agent, _ := row["agent_key"].(string)
		s.Agents = mergeAgentKeys(s.Agents, []string{agent})
		ts, _ := row["ts"].(string)
		stamp, e := time.Parse(time.RFC3339Nano, ts)
		if e != nil {
			return nil, e
		}
		if s.LastUsed == nil || stamp.After(*s.LastUsed) {
			s.LastUsed = &stamp
		}
	}
	flatten := func(set map[string]*SkillStat) []SkillStat {
		out := []SkillStat{}
		for _, s := range set {
			sort.Strings(s.Agents)
			out = append(out, *s)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Skill < out[j].Skill })
		return out
	}
	stats["skills"], stats["skill_stats"], stats["missing_skill_stats"] = skills, flatten(skillStats), flatten(missingStats)
	rows, err = decodeArchiveRows(tables["tool_usage"])
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		name, _ := row["tool_key"].(string)
		tools[name]++
	}
	stats["tools"] = tools
	findingStats := map[string]any{"total": 0, "pending": 0, "critical": 0, "high": 0, "medium": 0, "low": 0}
	classes := map[string]bool{}
	rows, err = decodeArchiveRows(tables["findings"])
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		severity, _ := row["severity"].(string)
		if severity == "" {
			severity = "unknown"
		}
		findings[severity]++
		findingStats["total"] = findingStats["total"].(int) + 1
		if _, ok := findingStats[severity]; ok {
			findingStats[severity] = findingStats[severity].(int) + 1
		}
		if row["status"] == "pending" {
			findingStats["pending"] = findingStats["pending"].(int) + 1
		}
		if c, _ := row["vulnclass"].(string); c != "" {
			classes[c] = true
		}
	}
	names := []string{}
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	findingStats["vulnclasses"] = names
	stats["findings"], stats["finding_stats"] = findings, findingStats
	return stats, nil
}

// Code-owned whitelist; package input never supplies identifiers.
var archiveSQLiteColumns = map[string]string{
	"task_intercept_rules":       "id:INTEGER task_id:INTEGER enabled:BOOLEAN action:TEXT kind:TEXT pattern:TEXT note:TEXT created_at:DATETIME updated_at:DATETIME",
	"main_sessions":              "exploration_id:INTEGER seq:INTEGER created_at:DATETIME",
	"tasks":                      "id:INTEGER name:TEXT category_id:INTEGER description:TEXT goal:TEXT exploration_id:INTEGER status:TEXT paused:BOOLEAN queued:BOOLEAN queued_at:DATETIME queue_mode:TEXT llm_profile_id:INTEGER active_llm_profile_id:INTEGER llm_chain_revision:INTEGER company_id:INTEGER parent_ref:TEXT timeout_seconds:INTEGER plan_heartbeat_seconds:INTEGER coverage_enabled:BOOLEAN pinned_at:DATETIME first_run_at:DATETIME deadline_at:DATETIME archived_at:DATETIME deleted_at:DATETIME completed_at:DATETIME created_at:DATETIME updated_at:DATETIME",
	"explorations":               "id:INTEGER description:TEXT goal:TEXT status:TEXT created_at:DATETIME updated_at:DATETIME round_no:INTEGER",
	"exploration_nodes":          "id:INTEGER exploration_id:INTEGER kind:TEXT payload:JSON priority:INTEGER state:TEXT origin:TEXT owner:TEXT blocked_reason:TEXT delete_reason:TEXT created_at:DATETIME updated_at:DATETIME completed_at:DATETIME content_version:INTEGER cold_since_round:INTEGER",
	"exploration_edges":          "exploration_id:INTEGER src_id:INTEGER dst_id:INTEGER rel:TEXT created_at:DATETIME",
	"exploration_anchors":        "node_id:INTEGER asset_id:INTEGER",
	"task_constraints":           "id:INTEGER exploration_id:INTEGER kind:TEXT text:TEXT origin:TEXT created_at:DATETIME updated_at:DATETIME",
	"activity":                   "id:INTEGER exploration_id:INTEGER node_id:INTEGER worker:TEXT kind:TEXT tool:TEXT tool_use_id:TEXT is_error:BOOLEAN summary:TEXT detail:TEXT metadata:JSON input_tokens:INTEGER output_tokens:INTEGER cache_read_tokens:INTEGER cache_write_tokens:INTEGER created_at:DATETIME main_seg:INTEGER",
	"task_relations":             "task_id:INTEGER source_task_id:INTEGER created_at:DATETIME",
	"task_asset_links":           "task_id:INTEGER asset_id:INTEGER source:TEXT source_summary:TEXT source_node_id:INTEGER created_at:DATETIME updated_at:DATETIME",
	"task_llm_profiles":          "task_id:INTEGER profile_id:INTEGER position:INTEGER status:TEXT last_error:TEXT exhausted_at:DATETIME created_at:DATETIME updated_at:DATETIME",
	"task_scope":                 "id:INTEGER task_id:INTEGER kind:TEXT company_id:INTEGER domain:TEXT net:TEXT value:TEXT source:TEXT reason:TEXT created_at:DATETIME",
	"findings":                   "id:INTEGER task_id:INTEGER node_id:INTEGER vulnclass:TEXT name:TEXT severity:TEXT summary:TEXT evidence:TEXT worker:TEXT asset_ids:JSON status:TEXT report:TEXT created_at:DATETIME evidence_version:INTEGER report_evidence_version:INTEGER",
	"finding_traffic_bindings":   "id:INTEGER finding_id:INTEGER snapshot_id:TEXT role:TEXT note:TEXT position:INTEGER created_at:DATETIME",
	"traffic_evidence_snapshots": "id:TEXT source_traffic_id:TEXT captured_at:INTEGER url:TEXT method:TEXT status:INTEGER content_type:TEXT req_head:TEXT resp_head:TEXT req_hash:TEXT resp_hash:TEXT req_len:INTEGER resp_len:INTEGER unreferenced_at:DATETIME created_at:DATETIME",
	"llm_records":                "id:INTEGER ts:DATETIME model:TEXT profile_name:TEXT session_id:TEXT task_id:TEXT worker:TEXT latency_ms:INTEGER input_tokens:INTEGER output_tokens:INTEGER cache_read:INTEGER cache_write:INTEGER status:TEXT error:TEXT request_body:TEXT response_body:TEXT raw_request:TEXT raw_response:TEXT",
	"llm_usage":                  "id:INTEGER ts:DATETIME task_id:TEXT exploration_id:INTEGER worker:TEXT model:TEXT profile_name:TEXT latency_ms:INTEGER input_tokens:INTEGER output_tokens:INTEGER cache_read:INTEGER cache_write:INTEGER status:TEXT",
	"skill_usage":                "id:INTEGER ts:DATETIME skill:TEXT agent_key:TEXT task_id:INTEGER exploration_id:INTEGER intent_id:INTEGER session_id:TEXT args_len:INTEGER found:BOOLEAN",
	"tool_usage":                 "id:INTEGER ts:DATETIME tool_key:TEXT agent_key:TEXT task_id:INTEGER exploration_id:INTEGER intent_id:INTEGER session_id:TEXT",
	"intercept_pending":          "id:INTEGER rule_id:INTEGER conversation_id:INTEGER task_id:TEXT agent_name:TEXT tool_name:TEXT tool_input:JSON status:TEXT reason:TEXT decided_at:DATETIME created_at:DATETIME audit:JSON decision_source:TEXT",
	"side_question_sessions":     "session_key:TEXT conversation_id:INTEGER task_id:INTEGER exploration_id:INTEGER intent_id:INTEGER run_id:INTEGER version:INTEGER snapshot:JSON generation:INTEGER memory:JSON",
	"side_question_requests":     "id:TEXT ordinal:INTEGER session_key:TEXT generation:INTEGER client_id:TEXT question:TEXT answer:TEXT status:TEXT error:TEXT model:JSON snapshot_at:DATETIME created_at:DATETIME sequence:INTEGER usage:JSON context_info:JSON",
	"assets":                     "id:INTEGER type:TEXT company_id:INTEGER company_source:TEXT task_ids:JSON domain:TEXT root_domain:TEXT ip:TEXT c_segment:TEXT port:INTEGER icp:TEXT bound_domains:JSON open_ports:JSON record_type:TEXT record_value:JSON bundle_id:TEXT app_name:TEXT category:TEXT app_description:TEXT app_icp:TEXT url:TEXT service_type:TEXT service_name:TEXT favicon_mmh3:TEXT status_code:INTEGER content_length:INTEGER page_title:TEXT technologies:JSON auth:JSON method:TEXT params:JSON extra:JSON created_at:DATETIME last_seen:DATETIME updated_at:DATETIME",
	"finding_retests":            "id:INTEGER finding_id:INTEGER conversation_id:INTEGER status:TEXT verdict:TEXT notes:TEXT snapshot:JSON summary:TEXT evidence:TEXT error:TEXT created_at:DATETIME started_at:DATETIME finished_at:DATETIME",
	"conversations":              "id:INTEGER agent_key:TEXT title:TEXT llm_profile_id:INTEGER pinned_at:DATETIME created_at:DATETIME updated_at:DATETIME",
	"conversation_activities":    "id:INTEGER conversation_id:INTEGER worker:TEXT kind:TEXT tool:TEXT tool_use_id:TEXT is_error:BOOLEAN summary:TEXT detail:TEXT input_tokens:INTEGER output_tokens:INTEGER cache_read_tokens:INTEGER cache_write_tokens:INTEGER created_at:DATETIME",
}
