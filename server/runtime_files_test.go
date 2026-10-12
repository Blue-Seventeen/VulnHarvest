package server

import (
	"bytes"
	"github.com/Autumn-27/artex/internal/testenv"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runtimeLink(t *testing.T, target, link string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Fatal("symlink unavailable")
	}
	if out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v %s", err, out)
	}
}
func TestRuntimeFileBoundary(t *testing.T) {
	base := testenv.LinkTempDir(t)
	data := filepath.Join(base, "data")
	state := filepath.Join(base, "state")
	skills := filepath.Join(base, "skills")
	for _, p := range []string{data, state, skills, filepath.Join(data, "inside"), filepath.Join(skills, "safe")} {
		os.MkdirAll(p, 0700)
	}
	os.WriteFile(filepath.Join(state, "jwt.key"), []byte("boundary-secret"), 0600)
	os.WriteFile(filepath.Join(data, "inside", "ok.txt"), []byte("allowed"), 0600)
	runtimeLink(t, state, filepath.Join(data, "escape"))
	runtimeLink(t, filepath.Join(data, "inside"), filepath.Join(data, "valid"))
	runtimeLink(t, state, filepath.Join(skills, "escape"))
	s := &Server{m: &Manager{dir: data}, skillDir: skills}
	for _, download := range []bool{false, true} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/?path=escape/jwt.key", nil)
		if download {
			s.wsDownload(w, r)
		} else {
			s.wsRead(w, r)
		}
		if w.Code == 200 || strings.Contains(w.Body.String(), "boundary-secret") {
			t.Fatalf("state escaped: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.wsRead(w, httptest.NewRequest("GET", "/?path=valid/ok.txt", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "allowed") {
		_, e := s.workspaceFS().Stat(filepath.Join(data, "valid", "ok.txt"))
		t.Fatalf("valid link blocked: %d %s (%v)", w.Code, w.Body.String(), e)
	}
	w = httptest.NewRecorder()
	s.wsWrite(w, httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"path":"escape/jwt.key","content":"overwrite"}`)))
	if w.Code == 200 {
		t.Fatal("state write escaped")
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("name", "escape")
	r.SetPathValue("file", "jwt.key")
	s.fsReadFile(w, r)
	if w.Code == 200 {
		t.Fatal("skill escaped")
	}
	b, _ := os.ReadFile(filepath.Join(state, "jwt.key"))
	if string(b) != "boundary-secret" {
		t.Fatal("state changed")
	}
	if path, msg := skillRelPath("脚本 子目录/文件 名.py"); msg != "" || strings.Contains(path, "\\") {
		t.Fatalf("slash contract: %q %q", path, msg)
	}
}
