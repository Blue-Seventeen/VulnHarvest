//go:build !windows

package runtimeenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tool "github.com/Autumn-27/norma/tool"
)

func runtimeAncestorAlias(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "ancestor alias")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestManagerPinsAncestorBeforeAliasRetarget(t *testing.T) {
	base := t.TempDir()
	alias := runtimeAncestorAlias(t, base)
	m := newTestManager(t, alias, "", time.Second)
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	captured := tool.ExecutionSnapshot().ChildPATH
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	if tool.ExecutionSnapshot().ChildPATH != captured {
		t.Fatal("retargeted ancestor changed pinned selection")
	}
	if _, err := os.Stat(filepath.Join(foreign, "独立 state")); !os.IsNotExist(err) {
		t.Fatal("manager followed retargeted ancestor")
	}
}

func TestManagerRejectsReplacedCanonicalRoot(t *testing.T) {
	base := t.TempDir()
	m := newTestManager(t, base, "", time.Second)
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	captured := tool.ExecutionSnapshot().ChildPATH
	rootPath := filepath.Dir(m.root)
	if err := os.Rename(rootPath, rootPath+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Check(context.Background(), Config{}); err == nil {
		t.Fatal("manager silently adopted replacement relay root")
	}
	if tool.ExecutionSnapshot().ChildPATH != captured {
		t.Fatal("rejected root replaced previous snapshot")
	}
}
