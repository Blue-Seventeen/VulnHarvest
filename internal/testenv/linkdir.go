package testenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// LinkTempDir keeps Windows junction fixtures on the checkout's NTFS volume.
// Large ordinary fixtures and compiler scratch still use the caller's TEMP.
func LinkTempDir(t testing.TB) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate owned junction fixture root")
	}
	root := filepath.Join(filepath.Dir(source), "..", "..", ".superpowers", "sdd", "portable-v0.0.1", "task-7a-ntfs-scratch")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}
