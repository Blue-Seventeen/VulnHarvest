//go:build !windows

package toolrelay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareRejectsCacheWithoutExecutePermission(t *testing.T) {
	state, source, entries := fixture(t)
	path, err := Prepare(context.Background(), state, source, entries)
	if err != nil {
		t.Fatal(err)
	}
	dirs := filepath.SplitList(path)
	manifest, err := os.Stat(filepath.Join(dirs[0], Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Mode().Perm() != 0600 {
		t.Fatalf("manifest mode=%o", manifest.Mode().Perm())
	}
	if _, err = Prepare(context.Background(), state, source, entries); err != nil {
		t.Fatalf("nonexecutable manifest rejected: %v", err)
	}
	alias := filepath.Join(dirs[1], "node")
	before, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(alias, 0600); err != nil {
		t.Fatal(err)
	}
	if err = exec.Command(alias).Run(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("actual launch error=%v want permission denied", err)
	}
	if _, err = Prepare(context.Background(), state, source, entries); err == nil {
		t.Fatal("nonexecutable cached helper accepted")
	}
	after, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if Hash(before) != Hash(after) {
		t.Fatal("immutable cache content changed")
	}
	info, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("immutable alias silently chmodded: %o", info.Mode().Perm())
	}
}
