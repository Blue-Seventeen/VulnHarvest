package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("NORMA_TEST_CHILD") == "1" {
		if p := os.Getenv("NORMA_TEST_MARKER"); p != "" {
			_ = os.WriteFile(p, []byte(strings.Join(os.Args, "|")), 0600)
		}
		fmt.Print(strings.Join(os.Args[1:], "|") + " PATH=" + os.Getenv("PATH"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Detects stale/frozen launch paths in synchronous and owned-background commands.
func TestExecutionSnapshotDrivesActualForegroundAndBackground(t *testing.T) {
	old := ExecutionSnapshot()
	defer SetExecutionRuntime(old)
	exe, _ := os.Executable()
	dir := t.TempDir()
	t.Setenv("NORMA_TEST_CHILD", "1")
	rt := &ExecutionRuntime{Generation: 1, ShellPath: exe, ShellArgs: []string{"selected-one"}, ChildPATH: dir, Commands: map[string]LaunchSpec{"rg": {Executable: exe}}}
	if err := SetExecutionRuntime(rt); err != nil {
		t.Fatal(err)
	}
	rt.ShellArgs[0] = "mutated"
	res, err := runBashSync(context.Background(), &ToolContext{Env: []string{"PATH=wrong"}}, "中文 空格", time.Second*5)
	if err != nil || !strings.Contains(res.Flatten(), "selected-one|中文 空格 PATH="+dir) {
		t.Fatalf("foreground %s %v", res.Flatten(), err)
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &Manager{tasks: map[string]*Task{}, sessions: map[string]*ptySession{}, dir: dir, rootCtx: root, rootCancel: cancel}
	defer manager.Cleanup()
	if err := SetExecutionRuntime(&ExecutionRuntime{Generation: 2, ShellPath: exe, ShellArgs: []string{"selected-two"}, ChildPATH: dir}); err != nil {
		t.Fatal(err)
	}
	task, err := manager.Spawn(SpawnSpec{Command: "argv 空格", WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-task.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background did not exit")
	}
	out, _ := manager.Output(task.ID, 0)
	if !strings.Contains(out, "selected-two|argv 空格") {
		t.Fatalf("background %s", out)
	}
	if RipgrepPath() != "" {
		t.Fatal("rescan retained old rg")
	}
	if err := SetExecutionRuntime(&ExecutionRuntime{ShellPath: "relative"}); err == nil {
		t.Fatal("relative snapshot accepted")
	}
	if ExecutionSnapshot().Generation != 2 {
		t.Fatal("failed publish replaced usable snapshot")
	}
}

// Detects npm auto-install on the SDK's legacy path, independently of detector probes.
func TestRipgrepNeverInstallsAndGoFallbackWorks(t *testing.T) {
	old := ExecutionSnapshot()
	defer SetExecutionRuntime(old)
	_ = SetExecutionRuntime(nil)
	dir := t.TempDir()
	exe, _ := os.Executable()
	b, _ := os.ReadFile(exe)
	name := "npm"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "npm-was-executed")
	t.Setenv("PATH", dir)
	t.Setenv("NORMA_TEST_CHILD", "1")
	t.Setenv("NORMA_TEST_MARKER", marker)
	t.Setenv("NORMA_RIPGREP_NO_INSTALL", "")
	t.Setenv("NORMA_DISABLE_RIPGREP", "")
	rgOnce = sync.Once{}
	_ = os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("fallback-needle"), 0600)
	res := run(t, NewGrep(), dir, map[string]any{"pattern": "fallback-needle", "output_mode": "files_with_matches"})
	if !strings.Contains(res.Flatten(), "sample.txt") {
		t.Fatal(res.Flatten())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("SDK executed npm instead of Go fallback")
	}
}
