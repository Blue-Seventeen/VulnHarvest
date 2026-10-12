package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeCleanupJoinsProcesses(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	t.Setenv("TMP", isolated)
	t.Setenv("TEMP", isolated)
	m, e := NewManager("runtime-owned-cleanup")
	if e != nil {
		t.Fatal(e)
	}
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	task, e := m.Spawn(SpawnSpec{Command: command})
	if e != nil {
		t.Fatal(e)
	}
	m.Cleanup()
	select {
	case <-task.done:
	default:
		t.Fatal("Cleanup returned before child/output writer joined")
	}
	if _, e = os.Stat(filepath.Dir(m.dir)); !os.IsNotExist(e) {
		t.Fatalf("Cleanup left task output directory: %v", e)
	}
	if _, e = m.Spawn(SpawnSpec{Command: command}); e == nil {
		t.Fatal("Spawn admitted after Cleanup")
	}
	m.Cleanup()
}
