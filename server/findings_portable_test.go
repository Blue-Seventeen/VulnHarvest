package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

func findingServerDB(t *testing.T) *db.DB {
	t.Helper()
	var d *db.DB
	var err error
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err = db.OpenSQLite(filepath.Join(t.TempDir(), "server.db"))
	} else {
		dsn := os.Getenv("ARTEX_FINDING_TEST_PG_DSN")
		if dsn == "" {
			t.Fatal("ARTEX_FINDING_TEST_PG_DSN required")
		}
		pool, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var name string
		if err := pool.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "vh_findings" {
			t.Fatalf("refusing reset of %q", name)
		}
		if _, err := pool.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
			t.Fatal(err)
		}
		d, err = db.Open(dsn)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func TestPortableFindingServerContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("Seed", testPortableFindingSeed)
			t.Run("ErrorClassification", testPortableFindingErrorClassification)
			t.Run("EvidenceHTTPExport", TestFindingTrafficAPIAndExport)
			t.Run("RetestStatus", TestRetestActiveStatusLifecycle)
			t.Run("RetestValidation", TestRetestValidationSeedingAndScope)
		})
	}
}

// Inject only owned stores into the real handlers. No NewManager, application
// startup, notification dispatcher, model runner, MCP or background service.
// Authentication/full lifecycle remain the runtime and final HTTP gate's scope.
func portableTrafficEvidenceServer(t *testing.T) (*Server, *db.RecordedFinding, func(string, string, string) *httptest.ResponseRecorder) {
	d := findingServerDB(t)
	dir := t.TempDir()
	tr, err := traffic.Open(filepath.Join(dir, "traffic"), ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	})
	m := &Manager{pg: d, dir: dir, traffic: tr, tasks: map[string]*Task{}}
	s := &Server{ctx: context.Background(), m: m}
	task, err := d.CreateTask("HTTP evidence fixture", "controlled local flow", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.evidenceStore().Record(t.Context(), db.RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, VulnClass: "TEST", Name: "Evidence API", Severity: "low", Summary: "bound evidence", Worker: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerFindingTraffic(mux)
	mux.HandleFunc("GET /api/exploration/findings/export", s.findingsExport)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	return s, f, request
}

func testPortableFindingSeed(t *testing.T) {
	d := findingServerDB(t)
	s := &Server{ctx: context.Background(), m: &Manager{pg: d, dir: t.TempDir(), tasks: map[string]*Task{}}}
	custom := json.RawMessage(`{ "required": ["custom"], "properties": {"custom":{"description":"user property","type":"string","x-user-large":9007199254740993}}, "type":"object" }`)
	for _, key := range []string{"report_finding", "update_finding_report", "add_hint", "add_task_hint", "traffic_search", "traffic_get", "traffic_blob", "get_finding_traffic", "bind_finding_traffic"} {
		if err := d.SeedTool(key, "custom description", custom, json.RawMessage(`["worker"]`)); err != nil {
			t.Fatal(err)
		}
		if err := d.UpdateTool(key, "custom description", custom, json.RawMessage(`["worker"]`), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.seedFindingRetester(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO agents(key,name,role) VALUES('reporter','fixture reporter','assistant') ON CONFLICT(key) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateTool("traffic_blob", "custom description", custom, json.RawMessage(`[ "pentest", "mainagent", "worker", "auto", "planner" ]`), false); err != nil {
		t.Fatal(err)
	}
	s.seedFindingTrafficTools()
	s.seedFindingWorkflowTools()
	for _, flag := range []string{"finding_retester_seed_v1", "finding_traffic_tools_v1", "finding_workflow_tools_v2_reporter"} {
		if v, _, err := d.GetSetting(flag); err != nil || v != "true" {
			t.Fatalf("seed flag %s=%q %v", flag, v, err)
		}
	}
	for key, fields := range map[string][]string{"report_finding": {"traffic_refs", "evidence_hint_id"}, "update_finding_report": {"evidence_version"}, "add_hint": {"traffic_refs", "hints"}, "add_task_hint": {"traffic_refs", "hints"}} {
		tool, err := d.GetTool(key)
		if err != nil || tool == nil {
			t.Fatal(key, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		props := schema["properties"].(map[string]any)
		for _, field := range fields {
			if props[field] == nil {
				t.Fatalf("%s missing %s: %s", key, field, tool.Schema)
			}
		}
		if tool.Enabled || tool.Description != "custom description" || props["custom"].(map[string]any)["description"] != "user property" {
			t.Fatalf("seed overwrote custom config: %+v", tool)
		}
		if !strings.Contains(string(tool.Schema), "9007199254740993") {
			t.Fatalf("seed rounded custom schema number: %s", tool.Schema)
		}
	}
	for _, key := range []string{"traffic_search", "traffic_get", "traffic_blob"} {
		tool, err := d.GetTool(key)
		if err != nil || tool == nil || len(tool.Agents) != 2 || !contains(tool.Agents, "reporter") || !contains(tool.Agents, "worker") {
			t.Fatalf("legacy reader binding: %+v %v", tool, err)
		}
	}
	worker, err := d.GetAgentByKey("worker")
	if err != nil || worker == nil {
		t.Fatal(err)
	}
	if _, err := d.SavePrompt(worker.ID, "USER PROMPT", "custom", "test"); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateTool("report_finding", "edited", json.RawMessage(`{"properties":{"traffic_refs":{"type":"string","description":"user override"}},"type":"object"}`), json.RawMessage(`["pentest"]`), false); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"get_finding_traffic", "bind_finding_traffic"} {
		if err := d.UpdateTool(key, "custom description", custom, json.RawMessage(`[ "pentest" ]`), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetSetting("finding_traffic_tools_v1", "false"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("finding_workflow_tools_v2_reporter", "false"); err != nil {
		t.Fatal(err)
	}
	s.seedFindingTrafficTools()
	s.seedFindingWorkflowTools()
	tool, err := d.GetTool("report_finding")
	if err != nil || tool == nil || tool.Description != "edited" || tool.Enabled || len(tool.Agents) != 1 || tool.Agents[0] != "pentest" || !strings.Contains(string(tool.Schema), "user override") {
		t.Fatalf("customized schema/binding changed: %+v %v", tool, err)
	}
	for _, key := range []string{"get_finding_traffic", "bind_finding_traffic"} {
		tool, err := d.GetTool(key)
		if err != nil || tool == nil || len(tool.Agents) != 1 || tool.Agents[0] != "pentest" || tool.Enabled || tool.Description != "custom description" {
			t.Fatalf("custom binding changed: %+v %v", tool, err)
		}
	}
	if err := d.RemoveAgentFromTool("reporter", "traffic_search"); err != nil {
		t.Fatal(err)
	}
	s.seedFindingTrafficTools()
	s.seedFindingWorkflowTools()
	search, err := d.GetTool("traffic_search")
	if err != nil || search == nil || contains(search.Agents, "reporter") {
		t.Fatalf("seed restored user unbinding: %+v %v", search, err)
	}
	if prompt, err := d.CurrentPrompt(worker.ID); err != nil || prompt != "USER PROMPT" {
		t.Fatal(prompt, err)
	}
	ret, err := d.GetAgentByKey(db.FindingRetestAgentKey)
	if err != nil || ret == nil {
		t.Fatal(err)
	}
	if _, err := d.SavePrompt(ret.ID, "CUSTOM RETEST PROMPT", "custom", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.seedFindingRetester(); err != nil {
		t.Fatal(err)
	}
	if prompt, err := d.CurrentPrompt(ret.ID); err != nil || prompt != "CUSTOM RETEST PROMPT" {
		t.Fatal(prompt, err)
	}
}

func testPortableFindingErrorClassification(t *testing.T) {
	d := findingServerDB(t)
	_, err := d.Exec(`INSERT INTO activity(exploration_id,kind) VALUES(-999,'test')`)
	if !isFKViolation(fmt.Errorf("wrapped: %w", err)) || !strings.HasPrefix(dropReason(err), "fk_violation") {
		t.Fatalf("native FK: %s %v", dropReason(err), err)
	}
	_, err = d.Exec(`INSERT INTO settings(key,value) VALUES('classifier','x'),('classifier','y')`)
	if !strings.HasPrefix(dropReason(err), "unique_violation") {
		t.Fatalf("native unique: %s %v", dropReason(err), err)
	}
	_, err = d.Exec(`UPDATE agents SET role='impossible' WHERE key='worker'`)
	if !strings.HasPrefix(dropReason(err), "check_violation") {
		t.Fatalf("native check: %s %v", dropReason(err), err)
	}
	if d.IsSQLite() {
		var seq int
		var alias, path string
		if err := d.QueryRow(`PRAGMA database_list`).Scan(&seq, &alias, &path); err != nil {
			t.Fatal(err)
		}
		peer, err := db.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()
		if _, err := peer.Exec(`PRAGMA busy_timeout=1`); err != nil {
			t.Fatal(err)
		}
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = peer.Exec(`INSERT INTO settings(key,value) VALUES('busy','x')`)
		if dropReason(err) != "busy_error" {
			t.Fatalf("native busy: %s %v", dropReason(err), err)
		}
	}
}
