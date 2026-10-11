package db

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func portableDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenSQLite(filepath.Join(t.TempDir(), "state.sqlite"))
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

func TestPortableSeedPreservesEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.GetAgentByKey("worker")
	if err != nil || a == nil {
		t.Fatalf("builtin missing: %v", err)
	}
	if _, err = d.Exec(`UPDATE agents SET name='my worker',description='my description',run_seconds=87,max_turns=9,interactive_shell=false WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE agent_prompt_vars SET description='my var',example='mine' WHERE agent_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SavePrompt(a.ID, "custom prompt\n{{ProxyAddr}}", "custom", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE mcp_servers SET args='["custom"]',env='{"TOKEN":"fixture"}' WHERE name='browser'`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE agent_skill_visibility SET enabled=false WHERE agent_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := d.GetAgentByKey("worker")
	vars, _ := d.PromptVars(a.ID)
	if _, err = d.Exec(`DELETE FROM settings WHERE key='interactive_shell_default_v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`DELETE FROM agents WHERE key='pentest'`); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	after, err := d.GetAgentByKey("worker")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("agent overwritten: %+v / %+v / %v", before, after, err)
	}
	afterVars, _ := d.PromptVars(a.ID)
	if !reflect.DeepEqual(vars, afterVars) {
		t.Fatalf("vars overwritten: %v / %v", vars, afterVars)
	}
	prompt, err := d.CurrentPrompt(a.ID)
	if err != nil || prompt != "custom prompt\n{{ProxyAddr}}" {
		t.Fatalf("prompt lost %q %v", prompt, err)
	}
	missing, err := d.GetAgentByKey("pentest")
	if err != nil || missing == nil {
		t.Fatalf("missing builtin not filled: %v", err)
	}
	names, err := d.AgentSkillNames(a.ID)
	if err != nil || len(names) != 0 {
		t.Fatalf("disabled skill resurrected: %v %v", names, err)
	}
	mcps, err := d.ListMCP()
	if err != nil {
		t.Fatal(err)
	}
	var browser, scope bool
	for _, m := range mcps {
		if m.Name == "browser" {
			browser = string(m.Args) == `["custom"]` && string(m.Env) == `{"TOKEN":"fixture"}`
		}
		if m.Name == "ScopeSentry" {
			scope = true
		}
	}
	if !browser || !scope {
		t.Fatalf("MCP preservation/default missing: %v", mcps)
	}
}

func TestPortableDateBindingsAndJSON(t *testing.T) {
	d := portableDB(t)
	p := &LLMProfile{Name: "fixture", Format: "openai", Model: "fixture", APIKey: "test-key", Streaming: true}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute).In(time.FixedZone("CST", 8*3600))
	if err = d.SaveLLMHealth(LLMHealth{ProfileID: id, OpenUntil: &past}); err != nil {
		t.Fatal(err)
	}
	health, err := d.LoadLLMHealth()
	if err != nil || len(health) != 0 {
		t.Fatalf("expired +08 health revived: %v %v", health, err)
	}
	future := time.Now().Add(time.Minute).In(time.FixedZone("west", -7*3600))
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE llm_profile_health SET open_until=$1 WHERE profile_id=$2`, future, id); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	health, err = d.LoadLLMHealth()
	if err != nil || len(health) != 1 || !health[0].OpenUntil.Equal(future) {
		t.Fatalf("future/native tx lost: %v %v", health, err)
	}
	var greater bool
	base := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)
	if err = d.QueryRow(`SELECT $1 > $2`, base.Add(time.Nanosecond), base).Scan(&greater); err != nil || !greater {
		t.Fatalf("fractional boundary: %v %v", greater, err)
	}
	tool := &Tool{Key: "fixture", Kind: "script", Schema: json.RawMessage(`{"type":"object"}`), Exec: json.RawMessage(`{"code":"fixture only"}`), Agents: []string{"worker"}, Enabled: false}
	if err = d.CreateCustomTool(tool); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTool(tool.Key)
	if err != nil || !reflect.DeepEqual(tool.Schema, got.Schema) || !reflect.DeepEqual(tool.Exec, got.Exec) {
		t.Fatalf("JSON roundtrip: %+v %v", got, err)
	}
}
