package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	tool "github.com/Autumn-27/norma/tool"
)

// Detects the real script executor ignoring the selected Python after rescan.
func TestRuntimePythonActualSelectionAndUnavailable(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("native Python not available")
	}
	old := tool.ExecutionSnapshot()
	defer tool.SetExecutionRuntime(old)
	code := `import json,sys,os; a=json.load(sys.stdin); print(a["target"]); print(os.environ["TOOL_TARGET"]); print(os.environ["HTTP_PROXY"])`
	rt := &tool.ExecutionRuntime{Generation: 1, ChildPATH: os.Getenv("PATH"), Commands: map[string]tool.LaunchSpec{"python": {Executable: python}}}
	if err := tool.SetExecutionRuntime(rt); err != nil {
		t.Fatal(err)
	}
	out, err := execPython(context.Background(), "missing-old-computer-python", "script中文", code, map[string]any{"target": "中文 space"}, t.TempDir(), []string{"HTTP_PROXY=http://127.0.0.1:49199"}, 10*time.Second)
	if err != nil || strings.Count(out, "中文 space") != 2 || !strings.Contains(out, "http://127.0.0.1:49199") {
		t.Fatalf("real Python %q %v", out, err)
	}
	rt.Generation = 2
	delete(rt.Commands, "python")
	_ = tool.SetExecutionRuntime(rt)
	if _, err := execPython(context.Background(), python, "missing", code, nil, t.TempDir(), nil, time.Second); err == nil {
		t.Fatal("missing selection fell back to supplied interpreter")
	}
}

// Child test server performs actual JSON-RPC initialize/tools/list over stdio.
func TestRuntimeServerMCPHelper(t *testing.T) {
	if os.Getenv("ARTEX_MCP_SENTINEL") != "1" {
		return
	}
	if marker := os.Getenv("ARTEX_MCP_ARGV_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte(strings.Join(os.Args, "|")+" CA="+os.Getenv("NODE_EXTRA_CA_CERTS")), 0600)
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req map[string]any
		_ = json.Unmarshal(sc.Bytes(), &req)
		if req["id"] == nil {
			continue
		}
		result := map[string]any{"protocolVersion": "2024-11-05"}
		if req["method"] == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "selected", "inputSchema": map[string]any{"type": "object"}}}}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}
	os.Exit(0)
}

func TestRuntimeMCPAdapterUsesSelectedCommand(t *testing.T) {
	exe, _ := os.Executable()
	old := tool.ExecutionSnapshot()
	defer tool.SetExecutionRuntime(old)
	rt := &tool.ExecutionRuntime{Generation: 1, ChildPATH: os.Getenv("PATH"), Commands: map[string]tool.LaunchSpec{"stale-custom-mcp": {Executable: exe, PrefixArgs: []string{"-test.run=TestRuntimeServerMCPHelper"}}}}
	_ = tool.SetExecutionRuntime(rt)
	env, _ := json.Marshal(map[string]string{"ARTEX_MCP_SENTINEL": "1"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := connectMCP(ctx, &db.MCPServer{Name: "sentinel", Transport: "stdio", Command: "stale-custom-mcp", Env: env})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ts, err := client.Tools(context.Background())
	if err != nil || len(ts) != 1 || ts[0].Name() != "mcp__sentinel__selected" {
		t.Fatalf("selected MCP handshake/list %v %v", ts, err)
	}
}

func TestRuntimeMCPDynamicOverlayIsNotPersisted(t *testing.T) {
	exe, _ := os.Executable()
	old := tool.ExecutionSnapshot()
	defer tool.SetExecutionRuntime(old)
	_ = tool.SetExecutionRuntime(&tool.ExecutionRuntime{Generation: 1, ChildPATH: os.Getenv("PATH"), Commands: map[string]tool.LaunchSpec{"custom-browser": {Executable: exe, PrefixArgs: []string{"-test.run=TestRuntimeServerMCPHelper"}}}})
	marker := filepath.Join(t.TempDir(), "argv")
	env, _ := json.Marshal(map[string]string{"ARTEX_MCP_SENTINEL": "1", "ARTEX_MCP_ARGV_MARKER": marker, "NODE_EXTRA_CA_CERTS": "old-computer-ca"})
	args, _ := json.Marshal([]string{"business 中文", "--proxy-server", "http://127.0.0.1:9"})
	server := &db.MCPServer{Name: "browser", Transport: "stdio", Command: "custom-browser", Env: env, Args: args}
	client, err := connectMCP(context.Background(), server, MCPLaunchOverlay{ProxyAddr: "http://127.0.0.1:49199", CACert: "new ca 中文"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	b, _ := os.ReadFile(marker)
	if !strings.Contains(string(b), "business 中文|--proxy-server|http://127.0.0.1:49199") || !strings.Contains(string(b), "CA=new ca 中文") || strings.Contains(string(b), "old-computer") || strings.Contains(string(b), "http://127.0.0.1:9|") {
		t.Fatalf("actual overlay %q", b)
	}
	if string(server.Env) != string(env) || string(server.Args) != string(args) {
		t.Fatal("dynamic overlay persisted into configuration")
	}
}
