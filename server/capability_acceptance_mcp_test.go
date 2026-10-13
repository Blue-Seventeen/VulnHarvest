package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

// Uses real Streamable HTTP and spawned stdio clients. A dropped visibility,
// enable flag, launch value, or shared Skill unlock set changes an observable
// handshake or tool receipt rather than just a database row.
func TestPortableReopenedMCPHTTPStdioAndSkillGate(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]map[string]int{}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.Header.Get("X-Acceptance-MCP") == "http-header-receipt" && r.Header.Get("Mcp-Session-Id") == "http-session-receipt" {
			w.WriteHeader(204)
			return
		}
		if r.Method != "POST" || r.Header.Get("X-Acceptance-MCP") != "http-header-receipt" {
			t.Errorf("MCP transport/header mismatch: %s %s header=%q", r.Method, r.URL.Path, r.Header.Get("X-Acceptance-MCP"))
			http.Error(w, "bad fixture request", 400)
			return
		}
		var req capabilityRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		if counts[r.URL.Path] == nil {
			counts[r.URL.Path] = map[string]int{}
		}
		counts[r.URL.Path][req.Method]++
		mu.Unlock()
		if req.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "http-session-receipt" {
			t.Error("real MCP client dropped initialize session id")
			http.Error(w, "missing session", 400)
			return
		}
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "http-session-receipt")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": capabilityMCPResult(req, "http:"+r.URL.Path)})
	}))
	defer fixture.Close()
	count := func(path, method string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[path][method]
	}
	h := newCapabilityHarness(t)
	api := controlledAPI(t, h.s)
	for _, key := range []string{"accept_mcp", "accept_mcp_other"} {
		api("POST", "/api/agents", map[string]any{"key": key, "name": key}, 200)
	}
	ids := map[string]int64{}
	for _, name := range []string{"direct", "gated", "disabled"} {
		id := api("POST", "/api/mcp", map[string]any{"name": "accept_" + name, "transport": "http", "url": fixture.URL + "/" + name, "env": map[string]string{"X-Acceptance-MCP": "http-header-receipt"}, "enabled": name != "disabled"}, 200)["id"].(float64)
		ids[name] = int64(id)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(h.base, "stdio-events.jsonl")
	argv := []string{"argument with spaces", "引号 \"receipt\"", "$literal-not-expanded"}
	stdioID := int64(api("POST", "/api/mcp", map[string]any{"name": "accept_stdio", "transport": "stdio", "command": exe, "args": argv, "env": map[string]string{"ARTEX_ACCEPTANCE_CHILD": "mcp-input-receipt", "ACCEPTANCE_MCP_MARKER": marker, "ACCEPTANCE_MCP_VALUE": "env value 中文"}, "enabled": true}, 200)["id"].(float64))
	api("POST", "/api/skills", map[string]any{"name": "accept-mcp-skill", "description": "unlock the controlled MCPs", "instructions": "MCP_SKILL_BODY_RECEIPT", "mcps": []string{"accept_gated", "accept_stdio", "accept_disabled"}}, 201)
	api("PUT", "/api/agents/accept_mcp/visibility", map[string]any{"mcp": []int64{ids["direct"], ids["gated"], ids["disabled"], stdioID}, "skill": []string{"accept-mcp-skill"}}, 200)
	if count("/direct", "initialize") != 1 || count("/gated", "initialize") != 1 || count("/disabled", "initialize") != 0 {
		t.Fatal("MCP create/discovery did not respect enablement")
	}
	h.reopen()
	if count("/direct", "initialize") != 1 || count("/gated", "initialize") != 1 || len(capabilityChildEvents(t, marker)) != 3 {
		t.Fatal("portable constructors auto-executed configured MCPs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, def, cleanup := agent.AugmentTools(ctx, "accept_mcp", nil)
	defer cleanup()
	reg := actool.NewRegistry(tools...)
	directName, gatedName, stdioName := "mcp__accept_direct__receipt", "mcp__accept_gated__receipt", "mcp__accept_stdio__receipt"
	if !reflect.DeepEqual(def.GlobalNames, []string{directName}) || !def.Unlock.Has(directName) || def.Unlock.Has(gatedName) || def.Unlock.Has(stdioName) {
		t.Fatalf("direct/global versus Skill gate assembled incorrectly: global=%v unlock=%v", def.GlobalNames, def.Unlock.List())
	}
	if _, ok := reg.Get("mcp__accept_disabled__receipt"); ok || contains(def.Deferred, "mcp__accept_disabled__receipt") {
		t.Fatal("disabled Skill-referenced MCP assembled")
	}
	search := actool.NewSearchExtraTools(reg, def.Deferred)
	res := capabilityCall(t, ctx, search, map[string]any{"query": "select:" + stdioName}, nil)
	if res.IsError || !strings.Contains(res.Flatten(), stdioName) || !strings.Contains(res.Flatten(), `"required":["value"]`) {
		t.Fatalf("real stdio tools/list schema absent from discovery: %s", res.Flatten())
	}
	execute := actool.NewExecuteExtraTool(reg, def.Unlock)
	for _, name := range []string{gatedName, stdioName} {
		res = capabilityCall(t, ctx, execute, map[string]any{"tool_name": name, "params": map[string]any{"value": "before-skill"}}, nil)
		if !res.IsError || !strings.Contains(res.Flatten(), "not unlocked") {
			t.Fatalf("Skill-gated MCP ran before Skill load: %s", res.Flatten())
		}
	}
	if count("/gated", "tools/call") != 0 || len(capabilityChildEvents(t, marker)) != 6 {
		t.Fatal("locked MCP call reached a real transport")
	}
	res = capabilityCall(t, ctx, execute, map[string]any{"tool_name": directName, "params": map[string]any{"value": "DIRECT_RECEIPT"}}, nil)
	if res.IsError || res.Flatten() != "http:/direct:DIRECT_RECEIPT" {
		t.Fatalf("globally visible MCP not callable before Skill load: %s", res.Flatten())
	}
	res = capabilityCall(t, ctx, capabilityTool(t, tools, "Skill"), map[string]any{"name": "accept-mcp-skill"}, nil)
	if res.IsError || len(res.Extra) != 1 || !strings.Contains(res.Extra[0].Text(), "MCP_SKILL_BODY_RECEIPT") || !strings.Contains(res.Flatten(), gatedName) || !strings.Contains(res.Flatten(), stdioName) || strings.Contains(res.Flatten(), "mcp__accept_disabled") {
		t.Fatalf("Skill failed to reveal only enabled gated MCPs: %+v", res)
	}
	for _, call := range []struct{ name, value, want string }{
		{gatedName, "HTTP_GATE_RECEIPT", "http:/gated:HTTP_GATE_RECEIPT"},
		{stdioName, "STDIO_GATE_RECEIPT", "stdio:env value 中文:STDIO_GATE_RECEIPT"},
	} {
		res = capabilityCall(t, ctx, execute, map[string]any{"tool_name": call.name, "params": map[string]any{"value": call.value}}, nil)
		if res.IsError || res.Flatten() != call.want {
			t.Fatalf("real %s call after Skill load: %s", call.name, res.Flatten())
		}
	}
	res = capabilityCall(t, ctx, execute, map[string]any{"tool_name": gatedName, "params": map[string]any{"value": 42}}, nil)
	if !res.IsError || !strings.Contains(res.Flatten(), "invalid params") || count("/gated", "tools/call") != 1 {
		t.Fatal("discovered schema failed to guard the actual MCP call")
	}
	cleanup()
	events := capabilityChildEvents(t, marker)
	if len(events) != 7 {
		t.Fatalf("stdio replay/count: got %d events, want two handshakes/lists plus one tools/call", len(events))
	}
	wantArgv := []any{argv[0], argv[1], argv[2]}
	methods := map[string]int{}
	for _, event := range events {
		if !reflect.DeepEqual(event["argv"], wantArgv) || event["env"] != "env value 中文" {
			t.Fatalf("persisted stdio argv/env not delivered literally: %v", event)
		}
		methods[event["method"].(string)]++
	}
	if methods["initialize"] != 2 || methods["notifications/initialized"] != 2 || methods["tools/list"] != 2 || methods["tools/call"] != 1 {
		t.Fatalf("real stdio protocol order/count missing: %v", methods)
	}
	other, otherDef, otherCleanup := agent.AugmentTools(ctx, "accept_mcp_other", nil)
	defer otherCleanup()
	if len(other) != 0 || len(otherDef.Deferred) != 0 || count("/direct", "initialize") != 2 || count("/gated", "initialize") != 2 {
		t.Fatal("invisible MCPs were spawned/assembled for another agent")
	}
	api = controlledAPI(t, h.s)
	api("POST", "/api/mcp", map[string]any{"id": ids["direct"], "name": "accept_direct", "transport": "http", "url": fixture.URL + "/direct", "env": map[string]string{"X-Acceptance-MCP": "http-header-receipt"}, "enabled": false}, 200)
	h.reopen()
	tools, def, closeAgain := agent.AugmentTools(ctx, "accept_mcp", nil)
	defer closeAgain()
	if _, ok := actool.NewRegistry(tools...).Get(directName); ok || contains(def.GlobalNames, directName) || count("/direct", "initialize") != 2 || count("/disabled", "initialize") != 0 {
		t.Fatal("persisted MCP disable failed to prevent real reconnection/advertisement")
	}
	cache := controlledAPI(t, h.s)("GET", fmt.Sprintf("/api/mcp/%d/tools", stdioID), nil, 200)
	encoded, _ := json.Marshal(cache)
	if !strings.Contains(string(encoded), stdioName) {
		t.Fatalf("API discovery cache absent after real constructors reopened: %s", encoded)
	}
}
