package tool

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimePTYSelectionOrHonestUnsupported(t *testing.T) {
	old := ExecutionSnapshot()
	defer SetExecutionRuntime(old)
	exe, _ := os.Executable()
	dir := t.TempDir()
	t.Setenv("NORMA_TEST_CHILD", "1")
	_ = SetExecutionRuntime(&ExecutionRuntime{Generation: 1, ShellPath: exe, ShellArgs: []string{"selected-pty"}, ChildPATH: dir})
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &Manager{tasks: map[string]*Task{}, sessions: map[string]*ptySession{}, dir: dir, rootCtx: root, rootCancel: cancel}
	defer manager.closeAllSessions()
	sess, err := manager.openSession("中文 argv", dir, nil, 24, 80, "dispatch", nil)
	if runtime.GOOS == "windows" {
		if err == nil || !strings.Contains(err.Error(), "不支持") {
			t.Fatalf("unsupported Windows PTY %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sess.done:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY child not joined")
	}
	text, _, _ := sess.readSince(0, 0, 4096, true)
	if !strings.Contains(text, "selected-pty|中文 argv") {
		t.Fatalf("PTY ignored published shell %q", text)
	}
}
