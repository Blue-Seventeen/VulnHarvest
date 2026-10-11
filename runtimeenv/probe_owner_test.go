package runtimeenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tool "github.com/Autumn-27/norma/tool"
)

// This is the executable fixture's own behavior, never passed as probe args.
func probeTreeSentinel(exe, kind string) bool {
	if !strings.HasPrefix(kind, "probe-tree-") {
		return false
	}
	marker := exe + ".heartbeat"
	if len(os.Args) > 1 && os.Args[1] == "--probe-heartbeat" {
		for {
			// Publish atomically: killing between truncate/write must not leave an
			// empty marker that could make a genuine leak assertion inconclusive.
			_ = os.WriteFile(marker+".new", []byte(fmt.Sprint(time.Now().UnixNano())), 0600)
			_ = os.Rename(marker+".new", marker)
			time.Sleep(5 * time.Millisecond)
		}
	}
	child := exec.Command(exe, "--probe-heartbeat")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	_ = os.WriteFile(exe+".child-pid", []byte(strconv.Itoa(child.Process.Pid)), 0600)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(4)
		}
		time.Sleep(5 * time.Millisecond)
	}
	switch kind {
	case "probe-tree-success":
		fmt.Print("sentinel 1.2.3")
		return true
	case "probe-tree-overflow":
		fmt.Print(strings.Repeat("x", 1<<20))
	}
	for {
		time.Sleep(time.Second)
	}
}

func waitProbeHeartbeat(t *testing.T, exe string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(exe + ".heartbeat"); err == nil && len(b) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("probe descendant never produced heartbeat")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cleanupProbeFixture(t *testing.T, exe string) {
	t.Helper()
	t.Cleanup(func() {
		b, _ := os.ReadFile(exe + ".child-pid")
		pid, _ := strconv.Atoi(string(b))
		if pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
				_, _ = p.Wait()
			}
		}
	})
}

func assertProbeStopped(t *testing.T, exe string) {
	t.Helper()
	waitProbeHeartbeat(t, exe)
	before, _ := os.ReadFile(exe + ".heartbeat")
	readyDeadline := time.Now().Add(time.Second)
	for len(before) == 0 && time.Now().Before(readyDeadline) {
		time.Sleep(time.Millisecond)
		before, _ = os.ReadFile(exe + ".heartbeat")
	}
	if len(before) == 0 {
		t.Fatal("probe heartbeat unavailable after cleanup")
	}
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		after, _ := os.ReadFile(exe + ".heartbeat")
		if len(after) > 0 && string(before) != string(after) {
			t.Fatal("owned probe descendant continued writing after probe and ToolManager.Close")
		}
	}
}

func TestProbeOwnerStopsDescendantsBeforeReturning(t *testing.T) {
	for _, tc := range []struct{ kind, wantVersion, wantReason string }{
		{"timeout", "", "version probe timeout/cancellation"},
		{"overflow", "", "version probe output limit exceeded"},
		{"success", "1.2.3", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			base := t.TempDir()
			exe := sentinel(t, filepath.Join(base, "probe 中文 space"), "node", "probe-tree-"+tc.kind)
			cleanupProbeFixture(t, exe)
			m := newTestManager(t, base, "", 2*time.Second)
			started := time.Now()
			version, reason := m.probe(context.Background(), tool.LaunchSpec{Executable: exe}, []string{"--version"})
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			// First assert actual post-return behavior; expected capability follows it.
			assertProbeStopped(t, exe)
			if version != tc.wantVersion || reason != tc.wantReason {
				t.Fatalf("probe capability %q/%q, want %q/%q", version, reason, tc.wantVersion, tc.wantReason)
			}
			if time.Since(started) > 8*time.Second {
				t.Fatal("probe owner exceeded timeout+cleanup bound")
			}
		})
	}
}

func TestProbeOwnerCancelledCheckKeepsSnapshotAndStopsDescendants(t *testing.T) {
	base := t.TempDir()
	oldNode := sentinel(t, filepath.Join(base, "old"), "node", "node")
	m := newTestManager(t, base, "", 5*time.Second)
	old, err := m.Check(context.Background(), Config{Paths: map[string]string{"node": oldNode}})
	if err != nil {
		t.Fatal(err)
	}
	exe := sentinel(t, filepath.Join(base, "new"), "node", "probe-tree-cancel")
	cleanupProbeFixture(t, exe)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Check(ctx, Config{Paths: map[string]string{"node": exe}}); done <- err }()
	waitProbeHeartbeat(t, exe)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancelled Check returned %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled Check did not join probe")
	}
	if snap := m.Snapshot(); snap.Generation != old.Generation || snap.Tools["node"].Path != oldNode || tool.ExecutionSnapshot().Generation != old.Generation {
		t.Fatal("cancelled Check replaced valid published generation")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	assertProbeStopped(t, exe)
}
