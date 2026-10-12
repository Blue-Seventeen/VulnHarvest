package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/runtimeenv"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeAuthenticatedCheckAndSessionResume(t *testing.T) {
	base := t.TempDir()
	relay := filepath.Join(base, "relay.exe")
	os.WriteFile(relay, []byte("file-only-relay-fixture"), 0700)
	tm, e := runtimeenv.New(runtimeenv.Options{BaseDir: base, StateDir: filepath.Join(base, "state"), RelayExecutable: relay})
	if e != nil {
		t.Fatal(e)
	}
	defer tm.Close()
	if _, e = tm.Check(context.Background(), runtimeenv.Config{}); e != nil {
		t.Fatal(e)
	}
	m, s := runtimeFixture(t, base)
	defer m.Close()
	defer s.Shutdown(context.Background())
	s.SetRuntime(tm, "127.0.0.1:38123", "127.0.0.1:38124")
	command := filepath.Join(base, "custom-business.cmd")
	marker := filepath.Join(base, "business-marker")
	os.WriteFile(command, []byte("echo started > \""+marker+"\""), 0700)
	id, e := m.pg.SaveMCP(&db.MCPServer{Name: "manual-only", Transport: "stdio", Command: command, Args: json.RawMessage(`["API-KEY-SECRET"]`), Env: json.RawMessage(`{"TOKEN":"MCP-ENV-SECRET"}`), Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	m.pg.SetSetting("python_interpreter", filepath.Join(base, "old-machine", "python.exe"))
	m.pg.SetSetting("notifier-secret", "LLM-KEY-SECRET")
	token, e := signJWT(s.jwtKey)
	if e != nil {
		t.Fatal(e)
	}
	request := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/runtime", ""}, {"POST", "/api/runtime/check", ""}, {"POST", "/api/runtime/resume", `{"features":["scheduler"]}`}} {
		if w := request(route.method, route.path, route.body, false); w.Code != 401 {
			t.Fatalf("unauth %s: %d", route.path, w.Code)
		}
	}
	w := request("POST", "/api/runtime/check", "", true)
	if w.Code != 200 {
		t.Fatalf("check %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"API-KEY-SECRET", "MCP-ENV-SECRET", "LLM-KEY-SECRET"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("runtime secret leak")
		}
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("rescan executed MCP business")
	}
	var info RuntimeInfo
	if e = json.Unmarshal(w.Body.Bytes(), &info); e != nil {
		t.Fatal(e)
	}
	if info.Tools.Generation != 2 || info.HTTP != "127.0.0.1:38123" || info.Mode != "portable" {
		t.Fatalf("runtime %+v", info)
	}
	python := info.Tools.Tools["python"]
	found := false
	for _, failure := range python.Failures {
		if failure.Source == "configured" {
			found = true
		}
	}
	if !found {
		t.Fatal("persisted interpreter never inspected as configured fallback candidate")
	}
	if w = request("POST", "/api/runtime/resume", `{"features":["notifier"]}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"notifier":true`) || !strings.Contains(w.Body.String(), "待投递") {
		t.Fatal("session restoration/pending effect absent")
	}
	if w = request("POST", "/api/runtime/resume", `{"features":["invalid"]}`, true); w.Code != 400 {
		t.Fatal("invalid feature accepted")
	}
	rows, e := m.pg.ListMCP()
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		if row.ID == id && (!strings.Contains(string(row.Env), "MCP-ENV-SECRET") || !strings.Contains(string(row.Args), "API-KEY-SECRET")) {
			t.Fatal("MCP config changed")
		}
	}
}
