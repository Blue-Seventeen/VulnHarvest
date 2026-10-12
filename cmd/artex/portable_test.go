package main

import (
	"encoding/json"
	"github.com/Autumn-27/artex/internal/testenv"
	"github.com/Autumn-27/artex/runtimeenv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPortableActualBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("actual binary build")
	}
	base := testenv.LinkTempDir(t)
	binary := filepath.Join(base, "artex")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	httpOwner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpOwner.Close()
	proxyOwner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyOwner.Close()
	cwd := testenv.LinkTempDir(t)
	os.WriteFile(filepath.Join(cwd, "config.json"), []byte(`{"mode":"postgres","database":{"dsn":"INVALID-CWD-DSN"}}`), 0600)
	env := append(os.Environ(), "ARTEX_CONFIG=", "ARTEX_PG_DSN=INVALID-PG-DSN-MUST-NOT-PARSE", "VULNHARVEST_MODE=postgres")
	state := filepath.Join(base, "state")
	os.MkdirAll(state, 0700)
	alias := filepath.Join(base, "state-alias")
	if runtime.GOOS == "windows" {
		if out, e := exec.Command("cmd", "/c", "mklink", "/J", alias, state).CombinedOutput(); e != nil {
			t.Fatalf("junction %v %s", e, out)
		}
	} else if e := os.Symlink(state, alias); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	for _, overlap := range []struct{ name, data, skills string }{
		{"data-base", base, filepath.Join(base, "skills")},
		{"data-state", state, filepath.Join(base, "skills")},
		{"data-alias", alias, filepath.Join(base, "skills")},
		{"skill-alias", filepath.Join(base, "data"), alias},
	} {
		rejected := exec.Command(binary, "-mode", "portable", "-no-browser", "-data", overlap.data, "-proxy", "")
		rejected.Dir = cwd
		rejected.Env = append(append([]string(nil), env...), "ARTEX_SKILL_DIR="+overlap.skills)
		out, e := rejected.CombinedOutput()
		if e == nil || !strings.Contains(string(out), "overlap") {
			t.Fatalf("%s CLI overlap did not reject: %v %s", overlap.name, e, out)
		}
		if _, e = os.Stat(filepath.Join(state, "main.sqlite")); !os.IsNotExist(e) {
			t.Fatalf("%s touched state DB before rejection", overlap.name)
		}
	}
	start := func() (*exec.Cmd, *os.File) {
		f, e := os.CreateTemp(testenv.LinkTempDir(t), "output-")
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(binary, "-mode", "portable", "-no-browser", "-addr", httpOwner.Addr().String(), "-proxy", proxyOwner.Addr().String())
		cmd.Dir = cwd
		cmd.Env = env
		cmd.Stdout = f
		cmd.Stderr = f
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		return cmd, f
	}
	cmd, output := start()
	defer func() { cmd.Process.Kill(); cmd.Wait(); output.Close() }()
	var info map[string]any
	deadline := time.Now().Add(120 * time.Second)
	runtimePath := filepath.Join(base, "state", "runtime.json")
	for time.Now().Before(deadline) {
		if _, e := os.Stat(runtimePath); os.IsNotExist(e) {
			_ = filepath.WalkDir(base, func(path string, d os.DirEntry, e error) error {
				if e == nil && d.Name() == "runtime.json" {
					runtimePath = path
				}
				return nil
			})
		}
		raw, _ := os.ReadFile(runtimePath)
		if json.Unmarshal(raw, &info) == nil && info["http_address"] != nil && info["ready"] == true {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if info["http_address"] == nil || info["ready"] != true {
		b, _ := os.ReadFile(output.Name())
		t.Fatalf("no ready runtime: %s", b)
	}
	address := info["http_address"].(string)
	if address == httpOwner.Addr().String() || info["proxy_address"] == proxyOwner.Addr().String() {
		t.Fatal("occupied port reused")
	}
	resp, err := http.Get("http://" + address + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp, err = http.Get("http://" + address + "/api/runtime")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("runtime unauth=%d", resp.StatusCode)
	}
	second, f := start()
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second instance succeeded")
		}
	case <-time.After(10 * time.Second):
		second.Process.Kill()
		t.Fatal("second instance did not reject lock")
	}
	f.Close()
	b, _ := os.ReadFile(f.Name())
	if !strings.Contains(string(b), "instance") {
		t.Fatalf("wrong second error: %s", b)
	}
	cmd.Process.Kill()
	cmd.Wait()
	output.Close()
	// The OS lock must be released by process death, with no stale-lock cleanup
	// heuristic. Re-acquire the exact state lock before starting another server.
	reacquired, e := runtimeenv.LockInstance(filepath.Join(base, "state", "instance.lock"))
	if e != nil {
		t.Fatalf("crash left stale lock: %v", e)
	}
	reacquired.Close()
}
