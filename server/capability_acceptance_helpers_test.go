package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

// The test executable is also a controlled child process. This exercises the
// production subprocess and protocol boundaries without depending on Node or
// Python being installed. The script child reports its inputs; it is deliberately
// not a Python implementation and does not claim to test Python language semantics.
func init() {
	switch os.Getenv("ARTEX_ACCEPTANCE_CHILD") {
	case "script-input-receipt":
		if len(os.Args) != 2 {
			os.Exit(71)
		}
		code, err := os.ReadFile(os.Args[1])
		if err != nil {
			os.Exit(72)
		}
		var params map[string]any
		if json.NewDecoder(os.Stdin).Decode(&params) != nil {
			os.Exit(73)
		}
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(74)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"code": string(code), "params": params, "cwd": cwd,
			"scalar_env": os.Getenv("TOOL_VALUE"), "session_env": os.Getenv("ACCEPTANCE_SESSION"),
			"encoding": os.Getenv("PYTHONIOENCODING"), "script": os.Args[1],
		})
		os.Exit(0)
	case "mcp-input-receipt":
		marker, err := os.OpenFile(os.Getenv("ACCEPTANCE_MCP_MARKER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(75)
		}
		defer marker.Close()
		initialized := false
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req capabilityRPCRequest
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				os.Exit(76)
			}
			_ = json.NewEncoder(marker).Encode(map[string]any{"method": req.Method, "argv": os.Args[1:], "env": os.Getenv("ACCEPTANCE_MCP_VALUE")})
			if req.Method == "notifications/initialized" {
				initialized = true
				continue
			}
			if req.Method != "initialize" && !initialized {
				os.Exit(77)
			}
			result := capabilityMCPResult(req, "stdio:"+os.Getenv("ACCEPTANCE_MCP_VALUE"))
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		os.Exit(0)
	}
}

type capabilityRPCRequest struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func capabilityMCPResult(req capabilityRPCRequest, receipt string) any {
	switch req.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "acceptance-fixture", "version": "1"}}
	case "tools/list":
		return map[string]any{"tools": []any{map[string]any{"name": "receipt", "description": "Controlled input receipt", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}}}}
	case "tools/call":
		var params struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.Name != "receipt" || params.Arguments["value"] == "" {
			return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "invalid receipt call"}}}
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": receipt + ":" + params.Arguments["value"]}}}
	default:
		return map[string]any{}
	}
}

type capabilityHarness struct {
	t                         *testing.T
	data, state, skills, base string
	m                         *Manager
	s                         *Server
}

func newCapabilityHarness(t *testing.T) *capabilityHarness {
	t.Helper()
	// No ambient provider key or process-wide tool selection may route an
	// acceptance test outside its own temporary directories and HTTP fixtures.
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ARTEX_LLM_BASE_URL", "ARTEX_LLM_API_KEY"} {
		t.Setenv(key, "")
	}
	oldPrompt, oldWrapup, oldWrapupTurns := agent.PromptOverride, agent.WrapupOverride, agent.WrapupMaxTurnsOverride
	oldTimeout, oldTimeoutTurns := agent.WrapupTaskTimeoutOverride, agent.WrapupTaskTimeoutTurnsOverride
	oldAugment, oldResolve, oldTraffic := agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled
	oldRuntime := actool.ExecutionSnapshot()
	if err := actool.SetExecutionRuntime(nil); err != nil {
		t.Fatal(err)
	}
	h := &capabilityHarness{t: t, base: t.TempDir()}
	h.data, h.state, h.skills = filepath.Join(h.base, "data"), filepath.Join(h.base, "state"), filepath.Join(h.base, "skills")
	if err := os.MkdirAll(h.skills, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.close()
		agent.PromptOverride, agent.WrapupOverride, agent.WrapupMaxTurnsOverride = oldPrompt, oldWrapup, oldWrapupTurns
		agent.WrapupTaskTimeoutOverride, agent.WrapupTaskTimeoutTurnsOverride = oldTimeout, oldTimeoutTurns
		agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled = oldAugment, oldResolve, oldTraffic
		if err := actool.SetExecutionRuntime(oldRuntime); err != nil {
			t.Error(err)
		}
	})
	h.open()
	return h
}

func (h *capabilityHarness) open() {
	h.t.Helper()
	var err error
	h.m, err = NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: h.data, StateDir: h.state, SkillDir: h.skills})
	if err != nil {
		h.t.Fatal(err)
	}
	h.s, err = NewWithOptions(context.Background(), h.m, h.skills, h.data, h.state)
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *capabilityHarness) close() {
	h.t.Helper()
	if h.s != nil {
		if err := h.s.Shutdown(context.Background()); err != nil {
			h.t.Error(err)
		}
		h.s = nil
	}
	if h.m != nil {
		if err := h.m.Close(); err != nil {
			h.t.Error(err)
		}
		h.m = nil
	}
}

func (h *capabilityHarness) reopen() {
	h.t.Helper()
	h.close()
	h.open()
}

func capabilityTool(t *testing.T, tools []actool.CoreTool, name string) actool.CoreTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("assembled tool %q missing", name)
	return nil
}

func capabilityCall(t *testing.T, ctx context.Context, tool actool.CoreTool, input any, tc *actool.ToolContext) actool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Call(ctx, raw, tc)
	if err != nil {
		t.Fatalf("%s: %v", tool.Name(), err)
	}
	return res
}

func capabilityJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("invalid JSON receipt %q: %v", text, err)
	}
	return value
}

func capabilityChildEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []map[string]any
	dec := json.NewDecoder(f)
	for {
		var event map[string]any
		err := dec.Decode(&event)
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatal(fmt.Errorf("child event: %w", err))
		}
		events = append(events, event)
	}
}
