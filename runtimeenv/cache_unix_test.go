//go:build !windows

package runtimeenv

import (
	"context"
	tool "github.com/Autumn-27/norma/tool"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckRejectsCacheWithoutExecutePermissionKeepsSnapshot(t *testing.T) {
	base := t.TempDir()
	node := sentinel(t, base, "node", "host")
	m := newTestManager(t, base, "", time.Second)
	cfg := Config{Paths: map[string]string{"node": node}}
	first, err := m.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := tool.ExecutionSnapshot()
	if first.Tools["node"].Status != "ready" {
		t.Fatalf("node detection %#v", first.Tools["node"])
	}
	alias := filepath.Join(filepath.SplitList(old.ChildPATH)[1], "node")
	if err = os.Chmod(alias, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Check(context.Background(), cfg); err == nil {
		t.Fatal("Check accepted nonexecutable helper cache")
	}
	current := tool.ExecutionSnapshot()
	if current.Generation != old.Generation || current.ChildPATH != old.ChildPATH || string(current.Metadata) != string(old.Metadata) || m.Snapshot().Generation != first.Generation {
		t.Fatal("failed Check changed prior SDK snapshot")
	}
	info, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("Check modified immutable alias permissions")
	}
}
