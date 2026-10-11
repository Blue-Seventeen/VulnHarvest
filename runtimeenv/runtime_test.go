package runtimeenv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/mcp"
	tool "github.com/Autumn-27/norma/tool"
)

func TestMain(m *testing.M) {
	if handled, code := RunRelay(); handled {
		os.Exit(code)
	}
	exe, _ := os.Executable()
	b, err := os.ReadFile(exe + ".sentinel")
	if err == nil {
		var kind string
		_ = json.Unmarshal(b, &kind)
		if probeTreeSentinel(exe, kind) {
			os.Exit(0)
		}
		if kind == "timeout" {
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		if kind == "overflow" {
			fmt.Print(strings.Repeat("x", 1<<20))
			os.Exit(0)
		}
		if kind == "fail" {
			os.Exit(2)
		}
		args := os.Args[1:]
		if len(args) > 0 && args[len(args)-1] == "--version" {
			if kind == "version-sensitive" {
				fmt.Print("token=must-not-expose sentinel 1.2.3")
				os.Exit(0)
			}
			fmt.Print("sentinel 1.2.3")
			os.Exit(0)
		}
		if len(args) > 0 && args[0] == "heartbeat" {
			if len(args) > 2 {
				_ = os.WriteFile(args[2], []byte(fmt.Sprint(os.Getpid())), 0600)
			}
			for {
				_ = os.WriteFile(args[1], []byte(fmt.Sprint(time.Now().UnixNano())), 0600)
				time.Sleep(10 * time.Millisecond)
			}
		}
		if marker := os.Getenv("SENTINEL_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte(strings.Join(args, "|")), 0600)
		}
		if kind == "rg" {
			fmt.Println(exe)
			os.Exit(0)
		}
		if kind == "mcp" {
			sc := bufio.NewScanner(os.Stdin)
			enc := json.NewEncoder(os.Stdout)
			for sc.Scan() {
				var req map[string]any
				_ = json.Unmarshal(sc.Bytes(), &req)
				if req["id"] == nil {
					continue
				}
				var res any
				if req["method"] == "initialize" {
					res = map[string]any{"protocolVersion": "2024-11-05"}
				} else {
					res = map[string]any{"tools": []any{map[string]any{"name": "sentinel", "inputSchema": map[string]any{"type": "object"}}}}
				}
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": res})
			}
			os.Exit(0)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"exe": exe, "args": args, "overlay": os.Getenv("NODE_EXTRA_CA_CERTS")})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sentinel(t *testing.T, dir, name, kind string) string {
	t.Helper()
	_ = os.MkdirAll(dir, 0700)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	exe, _ := os.Executable()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, b, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(kind)
	_ = os.WriteFile(p+".sentinel", b, 0600)
	return p
}

func newTestManager(t *testing.T, base, path string, timeout time.Duration) *ToolManager {
	t.Helper()
	exe, _ := os.Executable()
	m, err := New(Options{BaseDir: base, StateDir: filepath.Join(base, "独立 state"), HostPATH: path, RelayExecutable: exe, ProbeTimeout: timeout, ProbeOutputLimit: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

// A rejected bundled executable must not shadow host tools inside a real shell;
// rescanning must switch both direct execution and the shell's aliases together.
func TestCheckFallbackActualExecutionAndRescan(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host 中文 space")
	bundle := filepath.Join(base, "runtime", "bin")
	hostNode := sentinel(t, host, "node", "host")
	sentinel(t, bundle, "node", "fail")
	hostRG := sentinel(t, host, "rg", "rg")
	sentinel(t, bundle, "rg", "fail")
	sentinel(t, host, "npm", "npm")
	hostCurl := sentinel(t, host, "curl", "curl")
	installMarker := filepath.Join(base, "automatic-npm-business")
	t.Setenv("SENTINEL_MARKER", installMarker)
	shellName := "bash"
	if runtime.GOOS == "windows" {
		shellName = "powershell"
	}
	shell, err := exec.LookPath(shellName)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, base, host+string(os.PathListSeparator)+bundle, 5*time.Second)
	cfg := Config{Paths: map[string]string{"shell": shell, "python": hostNode}}
	snap, err := m.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["node"].Source != "path" || snap.Tools["node"].Path != hostNode || len(snap.Tools["node"].Failures) != 1 {
		t.Fatalf("fallback %#v", snap.Tools["node"])
	}
	if _, err := os.Stat(installMarker); !os.IsNotExist(err) {
		t.Fatal("check executed npm business command")
	}
	t.Setenv("SENTINEL_MARKER", "")
	launch, err := Command(context.Background(), "node", []string{"中文 arg", "space arg"}, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := launch.Output()
	if err != nil || !strings.Contains(string(out), hostNodeJSON(hostNode)) {
		t.Fatalf("direct %s %v", out, err)
	}
	tc := &tool.ToolContext{WorkingDir: base}
	grep, _ := tool.NewGrep().Call(context.Background(), []byte(`{"pattern":"anything","output_mode":"files_with_matches"}`), tc)
	if !strings.Contains(grep.Flatten(), hostRG) {
		t.Fatalf("real Grep ignored selected host rg: %s", grep.Flatten())
	}
	res, err := tool.NewBash().Call(context.Background(), []byte(`{"command":"node '中文 arg' 'space arg'"}`), tc)
	if err != nil || !strings.Contains(res.Flatten(), hostNodeJSON(hostNode)) || !strings.Contains(res.Flatten(), "中文 arg") {
		t.Fatalf("shell actual %s %v", res.Flatten(), err)
	}
	curlRes, _ := tool.NewBash().Call(context.Background(), []byte(`{"command":"curl '中文 arg' 'space arg'"}`), tc)
	if !strings.Contains(curlRes.Flatten(), hostNodeJSON(hostCurl)) {
		t.Fatalf("shell builtin alias bypassed selected curl: %s", curlRes.Flatten())
	}
	oldPATH := tool.ExecutionSnapshot().ChildPATH
	bundledNode := sentinel(t, bundle, "node", "bundle")
	bundledRG := sentinel(t, bundle, "rg", "rg")
	next, err := m.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != snap.Generation+1 || next.Tools["node"].Path != bundledNode {
		t.Fatalf("rescan %#v", next.Tools["node"])
	}
	res, _ = tool.NewBash().Call(context.Background(), []byte(`{"command":"node next"}`), tc)
	if !strings.Contains(res.Flatten(), hostNodeJSON(bundledNode)) {
		t.Fatal(res.Flatten())
	}
	grep, _ = tool.NewGrep().Call(context.Background(), []byte(`{"pattern":"anything","output_mode":"files_with_matches"}`), tc)
	if !strings.Contains(grep.Flatten(), bundledRG) {
		t.Fatalf("real Grep retained stale sync.Once: %s", grep.Flatten())
	}
	if _, err := os.Stat(strings.Split(oldPATH, string(os.PathListSeparator))[0]); err != nil {
		t.Fatal("rescan deleted old generation")
	}
	// Force alias preparation failure; the same published generation must survive.
	m.options.RelayExecutable = filepath.Join(base, "missing-relay")
	if _, err = m.Check(context.Background(), cfg); err == nil {
		t.Fatal("invalid relay accepted")
	}
	if m.Snapshot().Generation != next.Generation || tool.ExecutionSnapshot().Generation != next.Generation {
		t.Fatal("failed check changed selection")
	}
}

func hostNodeJSON(p string) string { b, _ := json.Marshal(p); return string(b) }

func TestCheckNeverStartsCustomMCPOrBrowserAndExplicitMCPWorks(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host")
	custom := sentinel(t, host, "custom-mcp", "mcp")
	browser := sentinel(t, host, "chrome", "fail")
	marker := filepath.Join(base, "business-executed")
	t.Setenv("SENTINEL_MARKER", marker)
	m := newTestManager(t, base, "", time.Second*5)
	snap, err := m.Check(context.Background(), Config{Paths: map[string]string{"chromium": browser}, MCPCommands: []string{custom}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["mcp:"+custom].Status != "unverified" || snap.Tools["chromium"].Status != "unverified" {
		t.Fatal("file checks claimed runnable")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("check started business command")
	}
	cmd, err := MCPCommand(custom, []string{"中文 business", "space arg"}, map[string]string{"NODE_EXTRA_CA_CERTS": "unpersisted-overlay"}, base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cl, err := mcp.NewStdioClientWithCommand(ctx, "sentinel", cmd)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ts, err := cl.Tools(context.Background())
	if err != nil || len(ts) != 1 {
		t.Fatalf("real MCP %v %v", ts, err)
	}
	b, _ := os.ReadFile(marker)
	if string(b) != "中文 business|space arg" {
		t.Fatalf("argv lost: %q", b)
	}
	serialized, _ := json.Marshal(m.Snapshot())
	if strings.Contains(string(serialized), "unpersisted-overlay") || strings.Contains(string(serialized), "business") {
		t.Fatal("snapshot exposed business args or env")
	}
}

func TestBoundedProbesFallBackAndUnavailableAliasBlocksRejectedPATH(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host")
	bundle := filepath.Join(base, "runtime", "bin")
	sentinel(t, bundle, "node", "timeout")
	hostNode := sentinel(t, host, "node", "host")
	sentinel(t, bundle, "rg", "overflow")
	m := newTestManager(t, base, bundle+string(os.PathListSeparator)+host, 2*time.Second)
	start := time.Now()
	snap, err := m.Check(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second*15 || snap.Tools["node"].Path != hostNode {
		t.Fatalf("unbounded probe or failed fallback: %#v", snap.Tools["node"])
	}
	if snap.Tools["rg"].Status != "missing" || len(snap.Tools["rg"].Failures) == 0 {
		t.Fatal("overflow capability not recorded")
	}
	alias := filepath.Join(strings.Split(tool.ExecutionSnapshot().ChildPATH, string(os.PathListSeparator))[0], "rg")
	if runtime.GOOS == "windows" {
		alias += ".exe"
	}
	cmd := exec.Command(alias, "business")
	cmd.Env = tool.RuntimeEnv(os.Environ(), nil, tool.ExecutionSnapshot().ChildPATH)
	if err := cmd.Run(); err == nil {
		t.Fatal("unavailable alias executed rejected rg")
	}
}

func TestWindowsKnownNpmWrapperUsesSelectedNodeAndKeepsExplicitArgs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows Node CLI wrapper gate")
	}
	base := t.TempDir()
	layout := filepath.Join(base, "npm 中文 space")
	node := sentinel(t, layout, "node", "node")
	_ = os.MkdirAll(filepath.Join(layout, "node_modules", "npm", "bin"), 0700)
	npm := filepath.Join(layout, "npm.cmd")
	npx := filepath.Join(layout, "npx.cmd")
	for _, p := range []string{npm, npx} {
		_ = os.WriteFile(p, []byte("@echo NEVER-RUN-WRAPPER"), 0600)
	}
	for _, name := range []string{"npm-cli.js", "npx-cli.js"} {
		_ = os.WriteFile(filepath.Join(layout, "node_modules", "npm", "bin", name), []byte("// local CLI"), 0600)
	}
	m := newTestManager(t, base, "", 5*time.Second)
	snap, err := m.Check(context.Background(), Config{Paths: map[string]string{"node": node, "npm": npm, "npx": npx}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["npm"].Status != "ready" || snap.Tools["npm"].Executable != node {
		t.Fatalf("wrapper %#v", snap.Tools["npm"])
	}
	cmd, err := MCPCommand("npx", []string{"--yes", "user-explicit-package", "中文 space"}, nil, base)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "npx-cli.js") || !strings.Contains(string(out), "user-explicit-package") || !strings.Contains(string(out), "中文 space") {
		t.Fatalf("known wrapper argv %s %v", out, err)
	}
}
