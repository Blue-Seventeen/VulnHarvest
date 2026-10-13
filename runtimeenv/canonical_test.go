package runtimeenv

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/toolrelay"
	tool "github.com/Autumn-27/norma/tool"
)

func TestCanonicalAncestorAliasExecutesCapturedRelay(t *testing.T) {
	base := t.TempDir()
	alias := runtimeAncestorAlias(t, base)
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	target := sentinel(t, canonical, "canonical-target", "host")
	m := newTestManager(t, alias, "", 5*time.Second)
	if _, err := m.Check(context.Background(), Config{Paths: map[string]string{"node": target}}); err != nil {
		t.Fatal(err)
	}
	path := tool.ExecutionSnapshot().ChildPATH
	wantRoot := filepath.Join(canonical, "独立 state", "runtime-path")
	if filepath.Dir(m.root) != wantRoot {
		t.Fatalf("session not anchored: %q, want %q", m.root, wantRoot)
	}
	dirs := filepath.SplitList(path)
	if filepath.Dir(filepath.Dir(dirs[1])) != wantRoot {
		t.Fatalf("PATH not anchored: %q", path)
	}
	c := exec.Command(filepath.Join(dirs[1], toolrelay.AliasName("node")), "中文 arg", "space arg")
	c.Env = tool.RuntimeEnv(os.Environ(), nil, path)
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("relay execution: %s %v", b, err)
	}
	var receipt struct {
		Exe  string   `json:"exe"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Exe != target || len(receipt.Args) != 2 || receipt.Args[0] != "中文 arg" || receipt.Args[1] != "space arg" {
		t.Fatalf("unexpected execution receipt: %s", b)
	}
}
