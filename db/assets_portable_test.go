package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Only this contract suite may reset the explicitly named disposable database.
func assetContractDB(t *testing.T) *DB {
	t.Helper()
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err := OpenSQLite(filepath.Join(t.TempDir(), "assets.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	dsn := os.Getenv("ARTEX_ASSET_TEST_PG_DSN")
	if dsn == "" {
		t.Fatal("ARTEX_ASSET_TEST_PG_DSN required for PostgreSQL contract")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var name string
	if err = pool.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "vh_assets" {
		t.Fatalf("refusing reset of %q", name)
	}
	if _, err = pool.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func openAssetTestDB(t *testing.T) (*DB, error) {
	if strings.HasPrefix(t.Name(), "TestPortableAssetContract/") {
		return assetContractDB(t), nil
	}
	return Open(testDSN(t))
}

func TestPortableAssetContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			if engine == "SQLite" {
				t.Run("PeerScopeMutation", testAssetPeerScopeMutation)
				t.Run("ScopeReadDuringWrite", testAssetScopeReadDuringWrite)
			} else {
				t.Run("PGScopeAdvisoryLock", TestAssetUpsertWaitsForCompanyScopeMutation)
			}
			t.Run("HostSideEffectFailureRollback", testAssetHostSideEffectFailure)
			t.Run("JSONPrecision", testAssetJSONPrecision)
			t.Run("AuthNumericSemanticDedup", testAssetAuthNumericDedup)
			t.Run("ArrayEscaping", testAssetArrayEscaping)
			t.Run("DSL", testAssetPortableDSL)
			t.Run("ScopedDSL", testAssetPortableScopedDSL)
			t.Run("StablePagination", TestAssetPaginationUsesStableIDTieBreaker)
			t.Run("ExistingScopeMembership", TestQueryDSLInScopeMembership)
			t.Run("HostDeletionCandidates", testAssetPortableHostCandidates)
			t.Run("DeleteAndSourceAssociations", testAssetDeleteAndSources)
			t.Run("ScopedAppendAndRollback", testAssetScopedAppendRollback)
			t.Run("ScopeRollback", testAssetScopeRollback)
			t.Run("AttributionPrecedenceAndWarnings", testAssetAttributionPrecedence)
			t.Run("UpsertRootDomain", TestUpsertRootDomain)
			t.Run("UpsertRootDomainEmpty", TestUpsertRootDomainEmpty)
			t.Run("UpsertIP", TestUpsertIP)
			t.Run("UpsertIPBoundDomains", TestUpsertIPBoundDomains)
			t.Run("AppendIPPort", TestAppendIPPort)
			t.Run("UpsertSubdomain", TestUpsertSubdomain)
			t.Run("UpsertSubdomainDedup", TestUpsertSubdomainDedup)
			t.Run("UpsertAppByBundle", TestUpsertAppByBundle)
			t.Run("UpsertAppByName", TestUpsertAppByName)
			t.Run("UpsertHTTPService", TestUpsertHTTPService)
			t.Run("UpsertHTTPServiceAuthAppend", TestUpsertHTTPServiceAuthAppend)
			t.Run("UpsertOtherService", TestUpsertOtherService)
			t.Run("UpsertOtherServiceAuthAppend", TestUpsertOtherServiceAuthAppend)
			t.Run("UpsertEndpoint", TestUpsertEndpoint)
			t.Run("UpsertEndpointRequiredFields", TestUpsertEndpointRequiredFields)
			t.Run("QueryByType", TestQueryByType)
			t.Run("DeleteByTaskID", TestDeleteByTaskID)
			t.Run("QueryByTask", TestQueryByTask)
			t.Run("QueryByTaskPaging", TestQueryByTaskPaging)
			t.Run("QueryByCompany", TestQueryByCompany)
			t.Run("QueryByCompanyPaging", TestQueryByCompanyPaging)
			t.Run("CountsByType", TestCountsByType)
			t.Run("CompanyAttributionAtInsertTime", TestCompanyAttributionAtInsertTime)
			t.Run("AssetIPRejectsHostname", TestAssetIPRejectsHostname)
			t.Run("CompanyUpsertAndGet", TestCompanyUpsertAndGet)
			t.Run("CreateCompanyWithScopeRejectsNormalizedDuplicate", TestCreateCompanyWithScopeRejectsNormalizedDuplicate)
			t.Run("CompanyScope", TestCompanyScope)
			t.Run("CompanyScopeInvalid", TestCompanyScopeInvalid)
			t.Run("ResolveCompany", TestResolveCompany)
			t.Run("UpdateScope", TestUpdateScope)
			t.Run("DeleteCompany", TestDeleteCompany)
			t.Run("DeleteCompanyWithAssetsDeletesBoth", TestDeleteCompanyWithAssetsDeletesBoth)
			t.Run("RecomputeAttribution", TestRecomputeAttribution)
			t.Run("ListCompanies", TestListCompanies)
			t.Run("ExplicitCompanyAttributionSurvivesScopeRebuild", TestExplicitCompanyAttributionSurvivesScopeRebuild)
			t.Run("CompanyICPAttribution", TestCompanyICPAttribution)
			t.Run("CompanyScopeAttribution", TestCompanyScopeAttribution)
			t.Run("ResolveAndRecomputeUseStableCompanyIDTieBreak", TestResolveAndRecomputeUseStableCompanyIDTieBreak)
			t.Run("CompanyScopeLimitsAndCheckedErrors", TestCompanyScopeLimitsAndCheckedErrors)
			t.Run("CompanyScopeCheckedReturnsSystemErrorSeparately", TestCompanyScopeCheckedReturnsSystemErrorSeparately)
		})
	}
}

func mustAsset(t *testing.T, id int64, err error) int64 {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func assertAsset(t *testing.T, s *AssetStore, id int64) *Asset {
	t.Helper()
	rows, err := s.GetByIDs([]int64{id})
	if err != nil || len(rows) != 1 {
		t.Fatalf("GetByIDs(%d): %v %v", id, rows, err)
	}
	return rows[0]
}

func testAssetJSONPrecision(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	first := map[string]any{"long_key": map[string]any{"n": json.Number("9007199254740993"), "nil": nil}, "a": "x"}
	second := map[string]any{"a": "x", "long_key": map[string]any{"nil": nil, "n": json.Number("9007199254740993")}}
	id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://json.example", Auth: []map[string]any{first}, TaskID: 9007199254740993})
	mustAsset(t, id, err)
	if _, err = d.Exec(d.SQL(`UPDATE assets SET auth=ARRAY[$1::jsonb] WHERE id=$2`, `UPDATE assets SET auth=json_array(json($1)) WHERE id=$2`), `{"long_key":{"n":9007199254740993,"nil":null},"a":"x"}`, id); err != nil {
		t.Fatal(err)
	}
	id2, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://json.example", Auth: []map[string]any{second}, TaskID: 9007199254740995})
	mustAsset(t, id2, err)
	a := assertAsset(t, s, id)
	if id2 != id || len(a.Auth) != 1 || !slices.Contains(a.TaskIDs, int64(9007199254740993)) || !slices.Contains(a.TaskIDs, int64(9007199254740995)) {
		t.Fatalf("dedup/tasks: %+v", a)
	}
	raw, _ := json.Marshal(a.Auth)
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("precision lost: %s", raw)
	}
	if _, err = d.Exec(d.SQL(`UPDATE assets SET extra=$1::jsonb WHERE id=$2`, `UPDATE assets SET extra=$1 WHERE id=$2`), `{"nested":{"before":1},"keep":null,"big":9007199254740993}`, id); err != nil {
		t.Fatal(err)
	}
	// Top-level || overwrites nested objects and retains null-valued keys.
	var merged json.RawMessage
	if err = d.QueryRow(d.SQL(`SELECT (extra || $1::jsonb)::text FROM assets WHERE id=$2`, `SELECT portable_json_overlay(extra,$1) FROM assets WHERE id=$2`), `{"nested":{"after":2},"right":null}`, id).Scan(JSONScanner{Target: &merged}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = decodeAssetJSON(merged, &m); err != nil {
		t.Fatal(err)
	}
	if m["big"] != json.Number("9007199254740993") || m["nested"].(map[string]any)["before"] != nil {
		t.Fatalf("overlay: %s", merged)
	}
	if _, ok := m["keep"]; !ok {
		t.Fatal("left null deleted")
	}
	if _, ok := m["right"]; !ok {
		t.Fatal("right null deleted")
	}
	// Key length ordering is decisive: ordinary json.Marshal chooses the opposite.
	p1 := map[string]any{"location": "query", "name": "q", "z": "aaa", "aa": "zzz", "precise": json.Number("9007199254740993")}
	p2 := map[string]any{"location": "query", "name": "q", "z": "zzz", "aa": "aaa", "precise": json.Number("9007199254740993")}
	ep, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/path", Method: "GET", Params: []map[string]any{p1}})
	mustAsset(t, ep, err)
	_, err = s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/path", Method: "GET", Params: []map[string]any{p2, {"location": "body", "name": "q", "value": "separate"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := assertAsset(t, s, ep)
	if len(got.Params) != 2 {
		t.Fatalf("params %+v", got.Params)
	}
	for _, p := range got.Params {
		if p["location"] == "query" && (p["z"] != "zzz" || p["precise"] != json.Number("9007199254740993")) {
			t.Fatalf("PG text tie-break: %+v", p)
		}
	}
	escaped, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/escape-param", Method: "GET", Params: []map[string]any{{"location": "query", "name": "escape", "value": "<"}}})
	mustAsset(t, escaped, err)
	_, err = s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/escape-param", Method: "GET", Params: []map[string]any{{"location": "query", "name": "escape", "value": "["}}})
	if err != nil {
		t.Fatal(err)
	}
	params := assertAsset(t, s, escaped).Params
	if len(params) != 1 || params[0]["value"] != "[" {
		t.Fatalf("jsonb text must not HTML-escape tie-break %+v", params)
	}
	unicodeParam, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/unicode-param", Method: "GET", Params: []map[string]any{{"location": "query", "name": "unicode", "value": "z"}}})
	mustAsset(t, unicodeParam, err)
	_, err = s.UpsertEndpoint(UpsertEndpointReq{URL: "https://json.example/unicode-param", Method: "GET", Params: []map[string]any{{"location": "query", "name": "unicode", "value": "\u2028"}}})
	if err != nil {
		t.Fatal(err)
	}
	params = assertAsset(t, s, unicodeParam).Params
	if len(params) != 1 || params[0]["value"] != "\u2028" {
		t.Fatalf("jsonb text Unicode line separator tie-break %+v", params)
	}
	ip, err := s.UpsertIP(UpsertIPReq{IP: "192.0.2.2", OpenPorts: []PortService{{Port: 443}}})
	mustAsset(t, ip, err)
	_, err = s.UpsertIP(UpsertIPReq{IP: "192.0.2.2", OpenPorts: []PortService{{Port: 443, Service: "https"}, {Port: 80, Service: "http"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AppendIPPort("192.0.2.2", 443, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.AppendIPBoundDomain("192.0.2.2", "json.example"); err != nil {
		t.Fatal(err)
	}
	got = assertAsset(t, s, ip)
	if len(got.OpenPorts) != 2 || len(got.BoundDomains) != 1 {
		t.Fatalf("ports/domains %+v", got)
	}
	for _, p := range got.OpenPorts {
		if fmt.Sprint(p["port"]) == "443" && p["service"] != "https" {
			t.Fatalf("service lost %+v", p)
		}
	}
}

func testAssetAuthNumericDedup(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	for _, kind := range []string{"http", "other"} {
		upsert := func(auth []map[string]any) (int64, error) {
			if kind == "http" {
				return s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://authnumbers.example", Auth: auth})
			}
			return s.UpsertOtherService(UpsertOtherServiceReq{IP: "192.0.2.77", Port: 22, ServiceName: "ssh", Auth: auth})
		}
		first := map[string]any{"n": json.Number("1"), "nested": map[string]any{"large": json.Number("9007199254740993")}}
		second := map[string]any{"nested": map[string]any{"large": json.Number("9007199254740993.0")}, "n": json.Number("1.0")}
		id, err := upsert([]map[string]any{first})
		mustAsset(t, id, err)
		_, err = upsert([]map[string]any{second})
		if err != nil {
			t.Fatal(err)
		}
		a := assertAsset(t, s, id)
		if len(a.Auth) != 1 {
			t.Fatalf("%s auth numeric semantic duplicates %+v", kind, a.Auth)
		}
		raw, _ := json.Marshal(a.Auth)
		if !strings.Contains(string(raw), "9007199254740993") {
			t.Fatalf("%s precision lost %s", kind, raw)
		}
	}
}

func testAssetArrayEscaping(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	value := `a"b\c_%`
	id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://escapes.example", Technologies: []string{value}})
	a := assertAsset(t, s, mustAsset(t, id, err))
	if !slices.Contains(a.Technologies, value) {
		t.Fatalf("technology string altered: %q want %q", a.Technologies, value)
	}
	id, err = s.UpsertIP(UpsertIPReq{IP: "192.0.2.55", BoundDomains: []string{value}})
	a = assertAsset(t, s, mustAsset(t, id, err))
	if !slices.Contains(a.BoundDomains, value) {
		t.Fatalf("bound domain string altered: %q want %q", a.BoundDomains, value)
	}
}

func testAssetPortableDSL(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	ids := []int64{}
	for i, title := range []string{"ÄBC 中文 ' quoted", "another"} {
		id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: fmt.Sprintf("https://dsl.example/%d", i), PageTitle: title, Technologies: []string{"ÄBC", "nginx"}, TaskID: 9007199254740993})
		ids = append(ids, mustAsset(t, id, err))
	}
	for _, c := range []struct {
		dsl  string
		want int
	}{{"", 2}, {`title="äbc"`, 1}, {`"äbc"`, 2}, {`technology=="nginx"`, 2}, {`technology!="nginx"`, 0}, {`technology="äbc"`, 2}, {`title="'"`, 1}, {`title="%"`, 2}, {`title="_"`, 2}, {`task_id==9007199254740993`, 2}, {`port>=443`, 2}, {`title="missing" OR technology=="nginx"`, 2}, {`root_domain=="dsl.example" OR title=="missing"`, 2}} {
		n, err := s.CountDSL(c.dsl, "service", 0)
		if err != nil || n != c.want {
			t.Fatalf("CountDSL %q: %d %v want %d", c.dsl, n, err, c.want)
		}
		rows, err := s.QueryDSL(c.dsl, "service", 0, 100, -2)
		if err != nil || len(rows) != n {
			t.Fatalf("QueryDSL %q: %d %v count %d", c.dsl, len(rows), err, n)
		}
	}
	for _, id := range ids {
		if _, err := d.Exec(`UPDATE assets SET last_seen=$1 WHERE id=$2`, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), id); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.QueryDSL("", "service", 9007199254740993, 1, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != ids[0] {
		t.Fatalf("stable cursor boundary %v %v", rows, err)
	}
	if _, err = s.QueryDSL("port>=bad", "", 0, 1, 0); err == nil {
		t.Fatal("invalid DSL accepted")
	}
}

// Seed minimal task rows directly: graph/context mutation belongs to Task3.
func seedAssetContractTask(t *testing.T, d *DB, name string) (int64, int64) {
	t.Helper()
	var exp, id int64
	if err := d.QueryRow(`INSERT INTO explorations(description,goal) VALUES($1,$1) RETURNING id`, name).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO tasks(name,description,goal,exploration_id) VALUES($1,$1,'test',$2) RETURNING id`, name, exp).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, exp
}

func testAssetPortableScopedDSL(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	task, _ := seedAssetContractTask(t, d, "local")
	src, _ := seedAssetContractTask(t, d, "source")
	indirect, _ := seedAssetContractTask(t, d, "indirect")
	for _, pair := range [][2]int64{{task, src}, {src, indirect}} {
		if _, err := d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, rule := range []struct {
		tid                          int64
		kind, domain, network, value string
	}{{task, "root_domain", "local.example", "", ""}, {src, "root_domain", "source.example", "", ""}, {indirect, "root_domain", "outside.example", "", ""}, {task, "cidr", "", "2001:db8::/32", ""}, {task, "icp", "", "", "icp42"}} {
		if _, err := d.Exec(d.SQL(`INSERT INTO task_scope(task_id,kind,domain,net,value) VALUES($1,$2,NULLIF($3,''),NULLIF($4,'')::cidr,NULLIF($5,''))`, `INSERT INTO task_scope(task_id,kind,domain,net,value) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''))`), rule.tid, rule.kind, rule.domain, rule.network, rule.value); err != nil {
			t.Fatal(err)
		}
	}
	in := []int64{}
	for _, url := range []string{"https://api.local.example/p", "https://api.source.example/p", "http://[2001:db8::42]/p"} {
		id, err := s.UpsertEndpoint(UpsertEndpointReq{URL: url, Method: "GET"})
		in = append(in, mustAsset(t, id, err))
	}
	app, err := s.UpsertApp(UpsertAppReq{Name: "scope app", ICP: " ICP 42 "})
	in = append(in, mustAsset(t, app, err))
	outside, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://outside.example/p", Method: "GET"})
	mustAsset(t, outside, err)
	got, err := s.GetByIDsInScope(task, append(slices.Clone(in), outside))
	if err != nil || len(got) != len(in) {
		t.Fatalf("scope membership %v %v", got, err)
	}
	rows, err := s.QueryDSLInScope("", "endpoint", task, 1, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != in[1] {
		t.Fatalf("scope pagination %v %v", rows, err)
	}
	// Out-of-scope rows must be filtered before LIMIT/OFFSET.
	rows, err = s.QueryDSLInScope("", "endpoint", task, 100, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("scope query %v %v", rows, err)
	}
	for _, a := range rows {
		if a.ID == outside {
			t.Fatal("indirect source scope leaked")
		}
	}
	count, err := s.CountDSLInScope("", "endpoint", task)
	if err != nil || count != len(rows) {
		t.Fatalf("scope count/query %d/%d %v", count, len(rows), err)
	}
}

func testAssetPortableHostCandidates(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	task, exp := seedAssetContractTask(t, d, "candidate")
	other, _ := seedAssetContractTask(t, d, "other")
	id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://shared.example/p", TaskID: task})
	mustAsset(t, id, err)
	_, err = s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://shared.example/p", TaskID: other})
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://exclusive.example/p", TaskID: task})
	mustAsset(t, exclusive, err)
	anchor, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: "anchored.example"})
	mustAsset(t, anchor, err)
	var node int64
	if err = d.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,state) VALUES($1,'fact','confirmed') RETURNING id`, exp).Scan(&node); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2)`, node, anchor); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.HostsForTaskDeletion(task, exp)
	if err != nil || !slices.Contains(hosts, "exclusive.example") || !slices.Contains(hosts, "anchored.example") || slices.Contains(hosts, "shared.example") {
		t.Fatalf("deletion candidates %v %v", hosts, err)
	}
	hosts, err = s.HostsByTask(task)
	if err != nil || !slices.Contains(hosts, "shared.example") {
		t.Fatalf("HostsByTask %v %v", hosts, err)
	}
	counts, err := s.CountsByTypeForTask(task)
	if err != nil || counts["service"] != 2 {
		t.Fatalf("counts %v %v", counts, err)
	}
}

func testAssetScopedAppendRollback(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	id, err := s.UpsertIP(UpsertIPReq{IP: "192.0.2.5"})
	mustAsset(t, id, err)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scoped := &AssetStore{db: d, company: d.Companies(), tx: tx}
	if err = scoped.AppendIPPort("192.0.2.5", 22, "ssh"); err != nil {
		t.Fatal(err)
	}
	if err = scoped.AppendIPBoundDomain("192.0.2.5", "rollback.example"); err != nil {
		t.Fatal(err)
	}
	staged := assertAsset(t, scoped, id)
	if len(staged.OpenPorts) != 1 || len(staged.BoundDomains) != 1 {
		t.Fatalf("scoped read lost uncommitted append %+v", staged)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	a := assertAsset(t, s, id)
	if len(a.OpenPorts) != 0 || len(a.BoundDomains) != 0 {
		t.Fatalf("rollback %+v", a)
	}
}

func testAssetScopeRollback(t *testing.T) {
	d := assetContractDB(t)
	c := d.Companies()
	id, _, err := c.UpsertCompany("rollback company", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = c.AddScopeInputsChecked(id, []ScopeInput{{Value: "before.example"}}, ""); err != nil {
		t.Fatal(err)
	}
	if d.IsSQLite() {
		if _, err = d.Exec(`CREATE TRIGGER asset_contract_scope_failure BEFORE INSERT ON company_scope WHEN NEW.raw='failure' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err = d.Exec(`CREATE FUNCTION asset_contract_scope_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.raw='failure' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER asset_contract_scope_failure BEFORE INSERT ON company_scope FOR EACH ROW EXECUTE FUNCTION asset_contract_scope_failure()`); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, err = c.UpdateScopeInputsChecked(id, []ScopeInput{{Kind: "keyword", Value: "failure"}}, "")
	if err == nil {
		t.Fatal("injected failure swallowed")
	}
	rules, err := c.GetScope(id)
	if err != nil || len(rules) != 1 || rules[0].Domain != "before.example" {
		t.Fatalf("replace rollback %v %v", rules, err)
	}
	_, _, _, _, _, err = c.CreateCompanyWithScope("must roll back", "", []ScopeInput{{Kind: "keyword", Value: "failure"}}, "")
	if err == nil {
		t.Fatal("create failure swallowed")
	}
	row, err := c.GetCompanyByName("must roll back")
	if err != nil || row != nil {
		t.Fatalf("create rollback %v %v", row, err)
	}
}

func testAssetAttributionPrecedence(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	c := d.Companies()
	companies := []int64{}
	for _, name := range []string{"broad", "narrow", "domain", "icp"} {
		id, _, err := c.UpsertCompany(name, "")
		if err != nil {
			t.Fatal(err)
		}
		companies = append(companies, id)
	}
	for i, rules := range [][]ScopeInput{{{Kind: "cidr", Value: "192.0.0.0/16"}, {Kind: "cidr", Value: "2001:db8::/32"}}, {{Kind: "cidr", Value: "192.0.2.0/24"}, {Kind: "cidr", Value: "2001:db8:1::/48"}}, {{Kind: "domain", Value: "precedence.example"}}, {{Kind: "icp", Value: " ICP 42 "}, {Kind: "keyword", Value: "only keyword"}}} {
		if _, _, _, _, err := c.AddScopeInputsChecked(companies[i], rules, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, address := range []string{"192.0.2.5", "2001:db8:1::5"} {
		id, err := s.UpsertIP(UpsertIPReq{IP: address})
		a := assertAsset(t, s, mustAsset(t, id, err))
		if a.CompanyID == nil || *a.CompanyID != companies[1] {
			t.Fatalf("longest prefix %+v", a)
		}
	}
	id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://precedence.example", IP: "192.0.2.5"})
	a := assertAsset(t, s, mustAsset(t, id, err))
	if a.CompanyID == nil || *a.CompanyID != companies[2] {
		t.Fatalf("domain precedence %+v", a)
	}
	var malformed int64
	if err = d.QueryRow(`INSERT INTO assets(type,ip) VALUES('ip','not-an-ip') RETURNING id`).Scan(&malformed); err != nil {
		t.Fatal(err)
	}
	if err = c.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	warning, err := c.MalformedIPAssetWarning()
	if err != nil || !strings.Contains(warning, fmt.Sprintf("#%d not-an-ip", malformed)) {
		t.Fatalf("warning %q %v", warning, err)
	}
	// Both ICP columns are considered together even when domain/IP are present
	// but unmatched. The lowest company ID wins after network/domain fail.
	if _, _, _, _, err = c.AddScopeInputsChecked(companies[0], []ScopeInput{{Kind: "icp", Value: "ICP low"}}, ""); err != nil {
		t.Fatal(err)
	}
	var dual int64
	if err = d.QueryRow(`INSERT INTO assets(type,domain,root_domain,ip,icp,app_icp) VALUES('subdomain','unmatched.example','unmatched.example','203.0.113.77','ICP42','ICP low') RETURNING id`).Scan(&dual); err != nil {
		t.Fatal(err)
	}
	if err = c.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	a = assertAsset(t, s, dual)
	if a.CompanyID == nil || *a.CompanyID != companies[0] {
		t.Fatalf("dual ICP tie-break %+v", a)
	}
	// A record IP beats ICP fallback; an exact root beats both.
	sub, err := s.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.unmatched.example", RecordType: "A", RecordValue: []string{"192.0.2.8"}, ICP: "ICP42"})
	a = assertAsset(t, s, mustAsset(t, sub, err))
	if a.CompanyID == nil || *a.CompanyID != companies[1] {
		t.Fatalf("subdomain IP before ICP %+v", a)
	}
	explicit, _, err := c.UpsertCompany("explicit no fallback", "")
	if err != nil {
		t.Fatal(err)
	}
	unmatched, err := s.UpsertApp(UpsertAppReq{Name: "explicit orphan", CompanyID: &explicit})
	mustAsset(t, unmatched, err)
	if err = c.DeleteCompany(explicit); err != nil {
		t.Fatal(err)
	}
	var source string
	if err = d.QueryRow(`SELECT company_source FROM assets WHERE id=$1`, unmatched).Scan(&source); err != nil || source != "explicit" {
		t.Fatalf("unmatched explicit provenance changed: %q %v", source, err)
	}
}

func testAssetHostSideEffectFailure(t *testing.T) {
	d := assetContractDB(t)
	if d.IsSQLite() {
		if _, err := d.Exec(`CREATE TRIGGER asset_contract_host_failure BEFORE INSERT ON assets WHEN NEW.type='root_domain' BEGIN SELECT RAISE(ABORT,'host failure'); END`); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := d.Exec(`CREATE FUNCTION asset_contract_host_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='root_domain' THEN RAISE EXCEPTION 'host failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER asset_contract_host_failure BEFORE INSERT ON assets FOR EACH ROW EXECUTE FUNCTION asset_contract_host_failure()`); err != nil {
			t.Fatal(err)
		}
	}
	_, err := d.Assets().UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://rollback.example"})
	if err == nil {
		t.Fatal("host side effect failure was swallowed")
	}
	var n int
	if err = d.QueryRow(`SELECT count(*) FROM assets`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partially committed side effects: %d %v", n, err)
	}
}

func testAssetPeerScopeMutation(t *testing.T) {
	d := assetContractDB(t)
	var seq int
	var name, file string
	if err := d.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	peer, err := OpenSQLite(file)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	old, _, err := d.Companies().UpsertCompany("old", "")
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := d.Companies().UpsertCompany("next", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = d.Companies().AddScopeInputsChecked(old, []ScopeInput{{Value: "peer.example"}}, ""); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM company_scope WHERE company_id=$1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err = insertScopeRuleTx(tx, next, ParsedScope{Kind: "domain", Domain: "peer.example", Raw: "peer.example"}, "", d); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  int64
		err error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		id, e := peer.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "peer.example"})
		done <- result{id, e}
	}()
	<-started
	select {
	case got := <-done:
		t.Fatalf("peer writer escaped immediate transaction: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		a := assertAsset(t, d.Assets(), got.id)
		if a.CompanyID == nil || *a.CompanyID != next {
			t.Fatalf("stale peer ownership %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not resume")
	}
}

func testAssetScopeReadDuringWrite(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	task, _ := seedAssetContractTask(t, d, "scope snapshot")
	if _, err := d.Exec(`INSERT INTO task_scope(task_id,kind,domain) VALUES($1,'root_domain','snapshot.example')`, task); err != nil {
		t.Fatal(err)
	}
	old, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://snapshot.example/old", Method: "GET", TaskID: task})
	mustAsset(t, old, err)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scoped := &AssetStore{db: d, company: d.Companies(), tx: tx}
	pending, err := scoped.UpsertEndpoint(UpsertEndpointReq{URL: "https://snapshot.example/new", Method: "GET", TaskID: task})
	mustAsset(t, pending, err)
	done := make(chan error, 1)
	go func() {
		n, e := s.CountDSLInScope("", "endpoint", task)
		if e != nil || n != 1 {
			done <- fmt.Errorf("committed scope count %d %v", n, e)
			return
		}
		rows, e := s.QueryDSLInScope("", "endpoint", task, 20, 0)
		if e != nil || len(rows) != 1 || rows[0].ID != old {
			done <- fmt.Errorf("committed scope query %v %v", rows, e)
			return
		}
		rows, e = s.GetByIDsInScope(task, []int64{old, pending})
		if e != nil || len(rows) != 1 || rows[0].ID != old {
			done <- fmt.Errorf("committed scope IDs %v %v", rows, e)
			return
		}
		_, e = s.HostsForTaskDeletion(task, 0)
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("pure scope CTE waited for the writer pool")
	}
	n, err := scoped.CountDSLInScope("", "endpoint", task)
	if err != nil || n != 2 {
		t.Fatalf("scoped CTE lost immediate transaction %d %v", n, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	n, err = s.CountDSLInScope("", "endpoint", task)
	if err != nil || n != 2 {
		t.Fatalf("committed update invisible %d %v", n, err)
	}
}

func testAssetDeleteAndSources(t *testing.T) {
	d := assetContractDB(t)
	s := d.Assets()
	task, exp := seedAssetContractTask(t, d, "remove")
	other, _ := seedAssetContractTask(t, d, "keep")
	only, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: "only.example", TaskID: task})
	mustAsset(t, only, err)
	shared, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: "shared.example", TaskID: task})
	mustAsset(t, shared, err)
	_, err = s.UpsertRootDomain(UpsertRootDomainReq{Domain: "shared.example", TaskID: other})
	if err != nil {
		t.Fatal(err)
	}
	var node int64
	if err = d.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,state) VALUES($1,'fact','confirmed') RETURNING id`, exp).Scan(&node); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE task_asset_links SET source='manual',source_summary='precise summary',source_node_id=$1 WHERE task_id=$2 AND asset_id=$3`, node, task, shared); err != nil {
		t.Fatal(err)
	}
	for _, query := range []func() ([]*Asset, error){func() ([]*Asset, error) { return s.QueryByTask(task, "root_domain", 10, 0) }, func() ([]*Asset, error) { return s.QueryDSL(`domain=="shared.example"`, "root_domain", task, 10, 0) }} {
		rows, err := query()
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, a := range rows {
			if a.ID == shared {
				found = true
				if a.TaskSource != "manual" || a.TaskSourceSummary != "precise summary" || a.TaskSourceNodeID == nil || *a.TaskSourceNodeID != node {
					t.Fatalf("source metadata %+v", a)
				}
			}
		}
		if !found {
			t.Fatal("source asset missing")
		}
	}
	if d.IsSQLite() {
		if _, err = d.Exec(`CREATE TRIGGER asset_contract_remove_failure BEFORE UPDATE ON assets WHEN NEW.domain='shared.example' BEGIN SELECT RAISE(ABORT,'remove failure'); END`); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err = d.Exec(`CREATE FUNCTION asset_contract_remove_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain='shared.example' THEN RAISE EXCEPTION 'remove failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER asset_contract_remove_failure BEFORE UPDATE ON assets FOR EACH ROW EXECUTE FUNCTION asset_contract_remove_failure()`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.DeleteByTaskID(task); err == nil {
		t.Fatal("detach failure swallowed")
	}
	assertAsset(t, s, only)
	a := assertAsset(t, s, shared)
	if !slices.Contains(a.TaskIDs, task) || !slices.Contains(a.TaskIDs, other) {
		t.Fatalf("detach failure committed %+v", a)
	}
	if _, err = d.Exec(d.SQL(`DROP TRIGGER asset_contract_remove_failure ON assets; DROP FUNCTION asset_contract_remove_failure()`, `DROP TRIGGER asset_contract_remove_failure`)); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteByTaskID(task)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteByTaskID %d %v", deleted, err)
	}
	a = assertAsset(t, s, shared)
	if len(a.TaskIDs) != 1 || a.TaskIDs[0] != other {
		t.Fatalf("shared asset changed %+v", a)
	}
	var n int
	if err = d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE asset_id=$1 AND task_id=$2`, shared, task).Scan(&n); err != nil || n != 0 {
		t.Fatalf("removed provenance %d %v", n, err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE asset_id=$1 AND task_id=$2`, shared, other).Scan(&n); err != nil || n != 1 {
		t.Fatalf("shared provenance lost %d %v", n, err)
	}
	counts, err := s.DeleteByHost(" SHARED.EXAMPLE. ")
	if err != nil || counts["root_domain"] != 1 {
		t.Fatalf("DeleteByHost %v %v", counts, err)
	}
	company, _, err := d.Companies().UpsertCompany("delete company", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		id, err := s.UpsertApp(UpsertAppReq{Name: name, CompanyID: &company})
		mustAsset(t, id, err)
	}
	rows, err := s.QueryByCompany(company, "app", 0, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("unlimited offset %v %v", rows, err)
	}
	deleted, err = s.DeleteByCompanyID(company)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteByCompanyID %d %v", deleted, err)
	}
	if rows, err = s.GetByIDs(nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty IDs %v %v", rows, err)
	}
	if deleted, err = s.DeleteByIDs(nil); err != nil || deleted != 0 {
		t.Fatalf("empty delete %d %v", deleted, err)
	}
}

func createAssetScopeTestTask(t *testing.T, d *DB, name string) (*Task, error) {
	if !strings.HasPrefix(t.Name(), "TestPortableAssetContract/") {
		return d.CreateTask(name, "goal", nil, 0, 0)
	}
	id, exp := seedAssetContractTask(t, d, name)
	return &Task{ID: id, ExplorationID: exp}, nil
}
func upsertAssetScopeTestRule(t *testing.T, d *DB, s *AssetStore, scope TaskScope) error {
	if !strings.HasPrefix(t.Name(), "TestPortableAssetContract/") {
		return s.upsertTaskScope(scope)
	}
	_, err := d.Exec(d.SQL(`INSERT INTO task_scope(task_id,kind,domain,net) VALUES($1,$2,NULLIF($3,''),NULLIF($4,'')::cidr)`, `INSERT INTO task_scope(task_id,kind,domain,net) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''))`), scope.TaskID, scope.Kind, scope.Domain, scope.Net)
	return err
}
