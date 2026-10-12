package db

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/internal/testenv"
	"testing"
	"time"
)

func TestPortableAssetReviewFixContract(t *testing.T) {
	for _, engine := range testenv.Engines(t) {
		t.Run(engine, func(t *testing.T) {
			t.Run("AssetUTCInstant", testReviewAssetUTC)
			t.Run("CompanyUTCInstant", testReviewCompanyUTC)
			t.Run("NumericText", testReviewNumericText)
			t.Run("NumericParamMerge", testReviewNumericMerge)
			t.Run("NullableTextParamIdentity", testReviewParamTextKeys)
			t.Run("MappedCompanyFamily", testReviewMappedCompany)
			t.Run("MappedDeclaredScopeFamily", testReviewMappedScope)
		})
	}
}

func reviewBoundInstant() time.Time {
	return time.Date(2026, 10, 1, 8, 15, 3, 123456000, time.FixedZone("non-UTC", 5*3600+1800))
}
func assertReviewAPIInstant(t *testing.T, d *DB, text string, want time.Time) {
	t.Helper()
	var encoded struct {
		Time string `json:"time"`
	}
	raw, err := json.Marshal(map[string]string{"time": text})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	layouts := []string{time.RFC3339Nano}
	if !d.IsSQLite() {
		layouts = append(layouts, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07")
	}
	var got time.Time
	for _, layout := range layouts {
		got, err = time.Parse(layout, encoded.Time)
		if err == nil {
			break
		}
	}
	if err != nil || !got.Equal(want) {
		t.Fatalf("API instant %q parses %v (%v), want %v", encoded.Time, got, err, want)
	}
}
func testReviewAssetUTC(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	id, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: "utc.example"})
	mustAsset(t, id, err)
	want := reviewBoundInstant()
	if _, err = d.Exec(`UPDATE assets SET last_seen=$1 WHERE id=$2`, want, id); err != nil {
		t.Fatal(err)
	}
	assertReviewAPIInstant(t, d, assertAsset(t, s, id).LastSeen, want.UTC())
	rows, err := s.QueryByType("root_domain", 5, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("QueryByType %v %v", rows, err)
	}
	assertReviewAPIInstant(t, d, rows[0].LastSeen, want.UTC())
}
func testReviewCompanyUTC(t *testing.T) {
	d := assetContractDB(t)
	s := d.Companies()
	id, _, err := s.UpsertCompany("UTC company", "")
	if err != nil {
		t.Fatal(err)
	}
	want := reviewBoundInstant()
	if _, err = d.Exec(`UPDATE companies SET created_at=$1,updated_at=$1 WHERE id=$2`, want, id); err != nil {
		t.Fatal(err)
	}
	// Both schemas' existing touch triggers set updated_at=now(); compare that
	// typed instant. created_at retains the explicitly bound non-UTC instant.
	var updated time.Time
	if err = d.QueryRow(`SELECT updated_at FROM companies WHERE id=$1`, id).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCompany(id)
	if err != nil {
		t.Fatal(err)
	}
	assertReviewAPIInstant(t, d, c.CreatedAt, want)
	assertReviewAPIInstant(t, d, c.UpdatedAt, updated)
	c, err = s.GetCompanyByName("utc COMPANY")
	if err != nil {
		t.Fatal(err)
	}
	assertReviewAPIInstant(t, d, c.CreatedAt, want)
	assertReviewAPIInstant(t, d, c.UpdatedAt, updated)
	rows, err := s.ListCompanies()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCompanies %v %v", rows, err)
	}
	assertReviewAPIInstant(t, d, rows[0].CreatedAt, want)
	assertReviewAPIInstant(t, d, rows[0].UpdatedAt, updated)
}
func testReviewNumericText(t *testing.T) {
	d := assetContractDB(t)
	for _, c := range []struct{ input, want string }{{"0.9e2", "90"}, {"0.09e3", "90"}, {"-0", "0"}, {"-0.0", "0.0"}, {"1.20e-2", "0.0120"}, {"1.20e2", "120"}, {"-0.0e-2", "0.000"}, {"9007199254740993.00", "9007199254740993.00"}, {"1.23456789012345678901234567890", "1.23456789012345678901234567890"}} {
		t.Run(c.input, func(t *testing.T) {
			got := pgJSONText(json.Number(c.input))
			if !d.IsSQLite() {
				if err := d.QueryRow(`SELECT $1::jsonb::text`, c.input).Scan(&got); err != nil {
					t.Fatal(err)
				}
			}
			if got != c.want {
				t.Fatalf("numeric text %s -> %s want %s", c.input, got, c.want)
			}
		})
	}
}
func testReviewNumericMerge(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	for i, c := range []struct{ a, b, want string }{{"0.9e2", "8", "90"}, {"0.09e3", "8", "90"}, {"-0", "-0.0", "0.0"}, {"1.20e-2", "0.0119", "0.0120"}, {"9007199254740993.00", "9007199254740992.99", "9007199254740993.00"}} {
		url := fmt.Sprintf("https://numbers.example/%d", i)
		for _, value := range []string{c.a, c.b} {
			id, err := s.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET", Params: []map[string]any{{"location": "query", "name": "q", "value": json.Number(value)}}})
			mustAsset(t, id, err)
		}
		rows, err := s.QueryDSL(`url=="`+url+`"`, "endpoint", 0, 5, 0)
		if err != nil || len(rows) != 1 || len(rows[0].Params) != 1 {
			t.Fatalf("param merge %v %v", rows, err)
		}
		if got := pgJSONText(rows[0].Params[0]["value"]); got != c.want {
			t.Fatalf("params numeric %s/%s chose %s want %s", c.a, c.b, got, c.want)
		}
	}
}
func testReviewParamTextKeys(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	cases := []struct {
		a, b   map[string]any
		count  int
		winner string
	}{
		{map[string]any{"location": "query", "name": json.Number("1"), "value": "numeric"}, map[string]any{"location": "query", "name": "1", "value": "string"}, 1, "numeric"},
		{map[string]any{"location": "query", "name": json.Number("0.9e2"), "value": "numeric"}, map[string]any{"location": "query", "name": "90", "value": "string"}, 1, "numeric"},
		{map[string]any{"location": "query", "name": json.Number("-0"), "value": "numeric"}, map[string]any{"location": "query", "name": "0", "value": "string"}, 1, "numeric"},
		{map[string]any{"location": "query", "name": json.Number("1.20e-2"), "value": "numeric"}, map[string]any{"location": "query", "name": "0.0120", "value": "string"}, 1, "numeric"},
		{map[string]any{"location": "query", "name": json.Number("1.0"), "value": "numeric"}, map[string]any{"location": "query", "name": "1", "value": "string"}, 2, ""},
		{map[string]any{"location": "query", "name": true, "value": "boolean"}, map[string]any{"location": "query", "name": "true", "value": "string"}, 1, "boolean"},
		{map[string]any{"location": "query", "name": []any{"x", json.Number("1")}, "value": "array"}, map[string]any{"location": "query", "name": `["x", 1]`, "value": "string"}, 1, "array"},
		{map[string]any{"location": "query", "name": map[string]any{"b": json.Number("1"), "aa": json.Number("2")}, "value": "object"}, map[string]any{"location": "query", "name": `{"b": 1, "aa": 2}`, "value": "string"}, 1, "object"},
		{map[string]any{"location": "query", "name": nil, "value": "null"}, map[string]any{"location": "query", "value": "missing"}, 1, "missing"},
		{map[string]any{"location": "query", "name": nil, "value": "null"}, map[string]any{"location": "query", "name": "null", "value": "string"}, 2, ""},
		{map[string]any{"location": json.Number("1"), "name": "q", "value": "numeric"}, map[string]any{"location": "1", "name": "q", "value": "string"}, 1, "string"},
	}
	for i, c := range cases {
		url := fmt.Sprintf("https://keys.example/%d", i)
		id, err := s.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET", Params: []map[string]any{c.a}})
		mustAsset(t, id, err)
		_, err = s.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET", Params: []map[string]any{c.b}})
		if err != nil {
			t.Fatal(err)
		}
		params := assertAsset(t, s, id).Params
		if len(params) != c.count || (c.winner != "" && params[0]["value"] != c.winner) {
			t.Fatalf("case %d ->> identity/tie-break %+v want count %d winner %s", i, params, c.count, c.winner)
		}
	}
}

func seedReviewIP(t *testing.T, d *DB, address string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(`INSERT INTO assets(type,ip) VALUES('ip',$1) RETURNING id`, address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func testReviewMappedCompany(t *testing.T) {
	d := assetContractDB(t)
	s := d.Companies()
	v4, _, err := s.UpsertCompany("IPv4", "")
	if err != nil {
		t.Fatal(err)
	}
	v6, _, err := s.UpsertCompany("IPv6", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = s.AddScopeInputsChecked(v4, []ScopeInput{{Kind: "cidr", Value: "192.0.2.0/24"}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = s.AddScopeInputsChecked(v6, []ScopeInput{{Kind: "cidr", Value: "2001:db8::/32"}}, ""); err != nil {
		t.Fatal(err)
	}
	mapped := "::ffff:192.0.2.5"
	resolved, err := s.ResolveCompany("", mapped)
	if err != nil || resolved != nil {
		t.Fatalf("mapped IPv6 matched IPv4: %v %v", resolved, err)
	}
	ids := []int64{seedReviewIP(t, d, "192.0.2.5"), seedReviewIP(t, d, "2001:db8::5"), seedReviewIP(t, d, mapped)}
	if err = s.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	a := assertAsset(t, d.Assets(), ids[2])
	if a.CompanyID != nil {
		t.Fatalf("mapped recompute matched v4 %+v", a)
	}
	for i, cid := range []int64{v4, v6} {
		a = assertAsset(t, d.Assets(), ids[i])
		if a.CompanyID == nil || *a.CompanyID != cid {
			t.Fatalf("family %d ownership %+v", i, a)
		}
	}
	if _, err = d.Exec(d.SQL(`INSERT INTO company_scope(company_id,kind,net,raw) VALUES($1,'cidr',$2::cidr,$2)`, `INSERT INTO company_scope(company_id,kind,net,raw) VALUES($1,'cidr',$2,$2)`), v6, "::ffff:192.0.2.0/120"); err != nil {
		t.Fatal(err)
	}
	resolved, err = s.ResolveCompany("", mapped)
	if err != nil || resolved == nil || *resolved != v6 {
		t.Fatalf("mapped family6 rule %v %v", resolved, err)
	}
	if err = s.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	a = assertAsset(t, d.Assets(), ids[2])
	if a.CompanyID == nil || *a.CompanyID != v6 {
		t.Fatalf("mapped family6 recompute %+v", a)
	}
}
func testReviewMappedScope(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	task, _ := seedAssetContractTask(t, d, "family scope")
	if _, err := d.Exec(d.SQL(`INSERT INTO task_scope(task_id,kind,net) VALUES($1,'cidr',$2::cidr)`, `INSERT INTO task_scope(task_id,kind,net) VALUES($1,'cidr',$2)`), task, "192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	v4 := seedReviewIP(t, d, "192.0.2.5")
	mapped := seedReviewIP(t, d, "::ffff:192.0.2.5")
	native := seedReviewIP(t, d, "2001:db8::5")
	var host int64
	if err := d.QueryRow(`INSERT INTO assets(type,domain,url,method) VALUES('endpoint','::ffff:192.0.2.5','http://[::ffff:192.0.2.5]/p','GET') RETURNING id`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryDSLInScope("", "", task, 20, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != v4 {
		t.Fatalf("IPv4 declared scope leaked mapped %v %v", rows, err)
	}
	count, err := s.CountDSLInScope("", "", task)
	if err != nil || count != 1 {
		t.Fatalf("scope count %d %v", count, err)
	}
	rows, err = s.GetByIDsInScope(task, []int64{v4, mapped, native, host})
	if err != nil || len(rows) != 1 {
		t.Fatalf("scope IDs %v %v", rows, err)
	}
	if _, err = d.Exec(d.SQL(`UPDATE task_scope SET net=$1::cidr WHERE task_id=$2`, `UPDATE task_scope SET net=$1 WHERE task_id=$2`), "::ffff:192.0.2.0/120", task); err != nil {
		t.Fatal(err)
	}
	rows, err = s.QueryDSLInScope("", "", task, 20, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("mapped family6 declared scope %v %v", rows, err)
	}
	for _, a := range rows {
		if a.ID != mapped && a.ID != host {
			t.Fatalf("mapped scope included other family %+v", a)
		}
	}
}
