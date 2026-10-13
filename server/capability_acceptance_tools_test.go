package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

// Catches edits that persist but are lost by hostTools/ToolResolve, default
// injection bypassed by custom executors, or filesystem skills assembled for
// agents that cannot see them.
func TestPortableReopenedToolsScriptsAndFilesystemSkills(t *testing.T) {
	var receiptCalls, forbiddenCalls atomic.Int32
	receipt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/receipt" || r.Method != "POST" || r.Header.Get("X-Acceptance-Value") != "默认值" {
			forbiddenCalls.Add(1)
			http.Error(w, "wrong rendered default or binding", 400)
			return
		}
		receiptCalls.Add(1)
		_, _ = w.Write([]byte("HTTP_DEFAULT_RECEIPT"))
	}))
	defer receipt.Close()
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	for _, key := range []string{"accept_tools", "accept_other"} {
		api("POST", "/api/agents", map[string]any{"key": key, "name": key}, 200)
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "default": "/old"}, "value": map[string]any{"type": "string", "default": "旧值"}}}
	for _, tool := range []struct {
		key     string
		agents  []string
		enabled bool
	}{
		{"accept_http", []string{"accept_tools"}, true},
		{"accept_disabled", []string{"accept_tools"}, false},
		{"accept_wrong_binding", []string{"accept_other"}, true},
	} {
		api("POST", "/api/tools/custom", map[string]any{"key": tool.key, "description": "old HTTP description", "schema": schema, "enabled": tool.enabled, "agents": tool.agents, "kind": "http", "deferred": true, "exec": map[string]any{"url": receipt.URL + "{path}", "method": "POST", "headers": map[string]string{"X-Acceptance-Value": "{value}"}, "timeout_ms": 2000}}, 200)
	}
	api("PUT", "/api/tools/accept_http", map[string]any{"description": "EDITED_HTTP_DESCRIPTION", "schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "default": "/receipt"}, "value": map[string]any{"type": "string", "default": "默认值"}}}, "enabled": true, "agents": []string{"accept_tools"}}, 200)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	api("PUT", "/api/settings", map[string]any{"python_interpreter": exe}, 200)
	const scriptCode = "# SCRIPT_CODE_RECEIPT\nprint('controlled script transport')\n"
	api("POST", "/api/tools/custom", map[string]any{"key": "accept_script", "description": "script input receipt", "schema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "default": "脚本默认值"}}}, "enabled": true, "agents": []string{"accept_tools"}, "kind": "script", "exec": map[string]any{"code": scriptCode, "timeout_ms": 4000}}, 200)
	for _, name := range []string{"accept-visible", "accept-hidden"} {
		api("POST", "/api/skills", map[string]any{"name": name, "description": name + " description", "instructions": "SKILL_BODY_RECEIPT Read ${SKILL_DIR}/references/receipt.md"}, 201)
		api("PUT", "/api/skills/"+name+"/files/references/receipt.md", map[string]any{"content": "FILESYSTEM_REFERENCE_RECEIPT " + name}, 200)
	}
	api("PUT", "/api/agents/accept_tools/visibility", map[string]any{"mcp": []int64{}, "skill": []string{"accept-visible"}}, 200)
	h.reopen()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, def, cleanup := agent.AugmentTools(ctx, "accept_tools", []actool.CoreTool{actool.NewRead()})
	defer cleanup()
	reg := actool.NewRegistry(tools...)
	for _, forbidden := range []string{"accept_disabled", "accept_wrong_binding"} {
		if _, ok := reg.Get(forbidden); ok {
			t.Fatalf("disabled/unbound tool assembled: %s", forbidden)
		}
		if contains(def.Deferred, forbidden) || contains(def.GlobalNames, forbidden) {
			t.Fatalf("disabled/unbound tool advertised: %s", forbidden)
		}
	}
	if !contains(def.Deferred, "accept_http") || !contains(def.GlobalNames, "accept_http") || !def.Unlock.Has("accept_http") {
		t.Fatal("custom deferred binding did not survive real server reopen")
	}
	search := actool.NewSearchExtraTools(reg, def.Deferred)
	discovery := capabilityCall(t, ctx, search, map[string]any{"query": "select:accept_http"}, nil)
	if discovery.IsError || !strings.Contains(discovery.Flatten(), "EDITED_HTTP_DESCRIPTION") || !strings.Contains(discovery.Flatten(), `"default":"/receipt"`) {
		t.Fatalf("edited description/schema did not reach deferred discovery: %s", discovery.Flatten())
	}
	execute := actool.NewExecuteExtraTool(reg, def.Unlock)
	res := capabilityCall(t, ctx, execute, map[string]any{"tool_name": "accept_http", "params": map[string]any{}}, nil)
	if res.IsError || capabilityJSON(t, res.Flatten())["body"] != "HTTP_DEFAULT_RECEIPT" || receiptCalls.Load() != 1 || forbiddenCalls.Load() != 0 {
		t.Fatalf("real native HTTP default receipt failed: %s count=%d forbidden=%d", res.Flatten(), receiptCalls.Load(), forbiddenCalls.Load())
	}
	scriptDir := filepath.Join(h.data, "sessions", "script-receipt")
	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	script := capabilityTool(t, tools, "accept_script")
	res = capabilityCall(t, ctx, script, map[string]any{}, &actool.ToolContext{WorkingDir: scriptDir, Env: []string{"ARTEX_ACCEPTANCE_CHILD=script-input-receipt", "ACCEPTANCE_SESSION=session-env-receipt"}})
	if res.IsError {
		t.Fatal(res.Flatten())
	}
	input := capabilityJSON(t, res.Flatten())
	// Getwd may return the physical spelling of an aliased working directory.
	// Compare the existing directory itself, preserving the exact CWD contract.
	cwdInfo, cwdErr := os.Stat(input["cwd"].(string))
	wantCWDInfo, wantCWDErr := os.Stat(scriptDir)
	if cwdErr != nil || wantCWDErr != nil {
		t.Fatalf("script CWD stat: got %v want %v", cwdErr, wantCWDErr)
	}
	if input["code"] != scriptCode || input["params"].(map[string]any)["value"] != "脚本默认值" || input["scalar_env"] != "脚本默认值" || input["session_env"] != "session-env-receipt" || input["encoding"] != "utf-8" || !os.SameFile(cwdInfo, wantCWDInfo) {
		t.Fatalf("production script executor lost code/default/stdin/env/CWD: %v", input)
	}
	wrongCWDInfo, err := os.Stat(h.data)
	if err != nil || os.SameFile(cwdInfo, wrongCWDInfo) {
		t.Fatalf("script CWD accepted a different existing directory: %v", err)
	}
	if _, err := os.Stat(input["script"].(string)); !os.IsNotExist(err) {
		t.Fatalf("production script executor did not clean its temporary .py: %v", err)
	}
	skill := capabilityTool(t, tools, "Skill")
	if !strings.Contains(skill.Prompt(), "accept-visible description") || strings.Contains(skill.Prompt(), "accept-hidden") {
		t.Fatalf("skill discovery ignored visibility: %s", skill.Prompt())
	}
	res = capabilityCall(t, ctx, skill, map[string]any{"name": "accept-hidden"}, nil)
	if !res.IsError || len(res.Extra) != 0 {
		t.Fatal("hidden filesystem skill was invokable")
	}
	res = capabilityCall(t, ctx, skill, map[string]any{"name": "accept-visible", "args": "CALLER_CONTEXT_RECEIPT"}, nil)
	referencePath := filepath.Join(h.skills, "accept-visible", "references", "receipt.md")
	// Interpolation replaces the directory variable, preserving the skill's
	// portable slash-separated reference suffix on Windows as well as Unix.
	guidanceReference := filepath.Join(h.skills, "accept-visible") + "/references/receipt.md"
	if res.IsError || len(res.Extra) != 1 || !strings.Contains(res.Extra[0].Text(), "SKILL_BODY_RECEIPT Read "+guidanceReference) || !strings.Contains(res.Extra[0].Text(), "CALLER_CONTEXT_RECEIPT") || strings.Contains(res.Extra[0].Text(), "${SKILL_DIR}") {
		t.Fatalf("Skill body/reference interpolation not consumed: %+v", res)
	}
	res = capabilityCall(t, ctx, capabilityTool(t, tools, "Read"), map[string]any{"file_path": referencePath}, &actool.ToolContext{WorkingDir: scriptDir})
	if res.IsError || !strings.Contains(res.Flatten(), "FILESYSTEM_REFERENCE_RECEIPT accept-visible") {
		t.Fatalf("real Read cannot consume the persisted skill reference: %s", res.Flatten())
	}
	other, _, otherCleanup := agent.AugmentTools(ctx, "accept_other", nil)
	defer otherCleanup()
	otherReg := actool.NewRegistry(other...)
	if _, ok := otherReg.Get("Skill"); ok {
		t.Fatal("unbound agent received filesystem skills")
	}
	if _, ok := otherReg.Get("accept_http"); ok {
		t.Fatal("custom tool agent binding leaked")
	}
	if _, ok := otherReg.Get("accept_wrong_binding"); !ok {
		t.Fatal("positive custom-tool binding was not assembled for its target agent")
	}
	// The ledger is a production side effect of the decorated tool and Skill calls.
	usage, _ := json.Marshal(controlledAPI(t, h.s)("GET", "/api/skills/accept-visible/usage", nil, 200))
	if !strings.Contains(string(usage), "accept_tools") {
		t.Fatalf("Skill invocation bypassed its persistent usage meter: %s", usage)
	}
}
