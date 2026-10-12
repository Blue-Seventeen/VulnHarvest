package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/testenv"
	"github.com/Autumn-27/artex/server"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

func TestPostgresActualBinaryRuntimeCheckAndSelection(t *testing.T) {
	dsn := testenv.RequirePostgres(t)
	testenv.ResetContract(t)
	pgcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid owned test configuration")
	}
	password := pgcfg.Password
	probe, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var database string
	if err = probe.QueryRow("SELECT current_database()").Scan(&database); err != nil || database != "vh_final" {
		probe.Close()
		t.Fatal("wrong dedicated database")
	}
	// Preserve PG's established recovery behavior; this isolated fixture must
	// have no enabled package MCP to avoid running unrelated business commands.
	var enabled int
	err = probe.QueryRow("SELECT count(*) FROM mcp_servers WHERE enabled=true").Scan(&enabled)
	probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatal("dedicated fixture must contain no enabled MCP servers")
	}
	base := t.TempDir()
	name := "artex"
	nodeName := "node"
	if runtime.GOOS == "windows" {
		name += ".exe"
		nodeName += ".exe"
	}
	binary := filepath.Join(base, name)
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %v: %s", err, output)
	}
	node := filepath.Join(base, "runtime", "bin", nodeName)
	os.MkdirAll(filepath.Dir(node), 0700)
	// A small native sentinel avoids loading the complete server/test binary
	// within the production 2-second probe ceiling on Windows AV-heavy hosts.
	sentinelSource := filepath.Join(t.TempDir(), "node.go")
	if err = os.WriteFile(sentinelSource, []byte(`package main
import("fmt";"os";"strings")
func main(){
 exe,_:=os.Executable(); raw,err:=os.ReadFile(exe+".sentinel");if err!=nil{os.Exit(2)}
 f,err:=os.OpenFile(string(raw),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil{os.Exit(2)}
 fmt.Fprintln(f,strings.Join(os.Args[1:],"|"));f.Close()
 if len(os.Args)>=2&&os.Args[len(os.Args)-1]=="--version"{fmt.Print("v22.0.0");return}
 fmt.Println("FIX1-SELECTED-NODE "+exe)
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", node, sentinelSource).CombinedOutput(); err != nil {
		t.Fatalf("build sentinel %v: %s", err, output)
	}
	audit := filepath.Join(base, "probe-audit")
	if err = os.WriteFile(node+".sentinel", []byte(audit), 0600); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close() // PG intentionally retains fixed-port semantics.
	output, err := os.CreateTemp(t.TempDir(), "postgres-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(binary, "-mode", "postgres", "-no-browser", "-addr", address, "-proxy", "")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ARTEX_CONFIG=", "ARTEX_PG_DSN="+dsn, "VULNHARVEST_MODE=postgres", "ARTEX_SKILL_DIR="+filepath.Join(base, "skills"))
	cmd.Stdout, cmd.Stderr = output, output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	url := "http://" + address
	// Check serializes fixed bounded probes for multiple tools and fallbacks;
	// the HTTP allowance must cover their aggregate, not one probe's timeout.
	client := &http.Client{Timeout: 60 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(output.Name())
		if strings.Contains(string(raw), "postgres ready: "+url) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		raw, _ := os.ReadFile(output.Name())
		t.Fatalf("no ready URL: %s", raw)
	}
	key, err := os.ReadFile(filepath.Join(base, "jwt.key"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, auth bool) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, url+path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, raw
	}
	if status, _ := request("GET", "/api/runtime", "", false); status != 401 {
		t.Fatalf("unauth runtime=%d", status)
	}
	status, raw := request("GET", "/api/runtime", "", true)
	var info server.RuntimeInfo
	if status != 200 || json.Unmarshal(raw, &info) != nil || info.Tools.Generation < 2 || len(info.Tools.Tools) == 0 {
		t.Fatalf("PG startup tools missing status=%d: %s", status, raw)
	}
	if info.Mode != "postgres" || info.HTTP != address || info.Tools.Tools["node"].Executable != node {
		t.Fatalf("wrong selected PG runtime: %s", raw)
	}
	generation := info.Tools.Generation
	status, raw = request("POST", "/api/runtime/check", "", true)
	if status != 200 || json.Unmarshal(raw, &info) != nil || info.Tools.Generation != generation+1 {
		t.Fatalf("PG rescan=%d: %s", status, raw)
	}
	if password != "" && strings.Contains(string(raw), password) {
		t.Fatal("runtime password leak")
	}
	auditRaw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(auditRaw)), "\n") {
		line = strings.TrimSpace(line)
		// Windows npm/npx wrappers legitimately use selected Node + their
		// known local CLI entrypoint with the same fixed version flag.
		entry, flag, wrapper := strings.Cut(line, "|")
		knownWrapper := wrapper && flag == "--version" && (filepath.Base(entry) == "npm-cli.js" || filepath.Base(entry) == "npx-cli.js")
		if line != "--version" && !knownWrapper {
			t.Fatalf("startup/check business invocation: %q", line)
		}
	}
	status, raw = request("POST", "/api/tools/custom/test", `{"kind":"command","exec":{"command":"node explicit-selection-check","timeout_ms":3000}}`, true)
	if status != 200 || !strings.Contains(string(raw), "FIX1-SELECTED-NODE") || !strings.Contains(string(raw), nodeName) {
		t.Fatalf("actual selected tool execution=%d %s", status, raw)
	}
	auditRaw, err = os.ReadFile(audit)
	if err != nil || !strings.Contains(string(auditRaw), "explicit-selection-check") {
		t.Fatalf("explicit selected node not launched: %v", err)
	}
	if _, err = os.Stat(filepath.Join(base, "state", "main.sqlite")); !os.IsNotExist(err) {
		t.Fatal("PG created portable main SQLite")
	}
}
