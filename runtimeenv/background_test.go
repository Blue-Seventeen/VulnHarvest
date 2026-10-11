package runtimeenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	tool "github.com/Autumn-27/norma/tool"
)

func TestBackgroundRelayKillStopsActualTarget(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host")
	sentinel(t, host, "node", "node")
	shellName := "bash"
	if runtime.GOOS == "windows" {
		shellName = "powershell"
	}
	shell, err := exec.LookPath(shellName)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, base, host, 5*time.Second)
	if _, err := m.Check(context.Background(), Config{Paths: map[string]string{"shell": shell}}); err != nil {
		t.Fatal(err)
	}
	tm, err := tool.NewManager(fmt.Sprintf("portable-sentinel-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Cleanup()
	marker := filepath.Join(base, "owned-heartbeat")
	task, err := tm.Spawn(tool.SpawnSpec{Command: "node heartbeat '" + marker + "'", WorkingDir: base})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay target did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tm.Kill(task.ID)
	_, finished := tm.Wait(context.Background(), task.ID, 5*time.Second)
	if !finished {
		t.Fatal("killed shell was not joined")
	}
	before, _ := os.ReadFile(marker)
	time.Sleep(150 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if string(before) != string(after) {
		t.Fatal("alias child survived norma background tree kill")
	}
}

func TestSynchronousRelayTimeoutStopsActualTarget(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host")
	sentinel(t, host, "node", "node")
	shellName := "bash"
	if runtime.GOOS == "windows" {
		shellName = "powershell"
	}
	shell, err := exec.LookPath(shellName)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, base, host, 5*time.Second)
	if _, err := m.Check(context.Background(), Config{Paths: map[string]string{"shell": shell}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(base, "sync-heartbeat")
	pidPath := filepath.Join(base, "pid")
	// Always clean the exact sentinel process if the red test proves a leak.
	defer func() {
		b, _ := os.ReadFile(pidPath)
		pid, _ := strconv.Atoi(string(b))
		if pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	}()
	raw, _ := json.Marshal(map[string]any{"command": "node heartbeat '" + marker + "' '" + pidPath + "'", "timeout_ms": 2500})
	res, err := tool.NewBash().Call(context.Background(), raw, &tool.ToolContext{WorkingDir: base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("target never started: %s", res.Flatten())
	}
	before, _ := os.ReadFile(marker)
	time.Sleep(150 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if string(before) != string(after) {
		t.Fatal("relay target kept running after foreground timeout returned")
	}
}
