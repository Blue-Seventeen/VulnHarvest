package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerRowCleanupStopsOwnerBeforeRows(t *testing.T) {
	testManagerRowCleanupOrder(t, "")
}

func TestManagerRowCleanupAfterFatal(t *testing.T) {
	if marker := os.Getenv("VH_FIXTURE_FATAL_MARKER"); marker != "" {
		testManagerRowCleanupOrder(t, marker)
		t.Fatal("expected fixture fatal")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "cleanup-order.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestManagerRowCleanupAfterFatal$", "-test.v")
	// The early-Fatal child owns a separate SQLite file, so it never contends
	// with a PostgreSQL parent suite's pinned advisory lock.
	child.Env = append(os.Environ(), "ARTEX_TEST_BACKEND=sqlite", "ARTEX_PG_DSN=", "VH_FIXTURE_FATAL_MARKER="+marker)
	output, childErr := child.CombinedOutput()
	if childErr == nil || ctx.Err() != nil || !strings.Contains(string(output), "expected fixture fatal") {
		t.Fatalf("fatal fixture child: err=%v output=%s", childErr, output)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "owner joined\nrows deleted\nstore closed\n" {
		t.Fatalf("fatal cleanup order=%q err=%v output=%s", got, err, output)
	}
}

func testManagerRowCleanupOrder(t *testing.T, marker string) {
	t.Helper()
	var m *Manager
	appendMarker := func(line string) error {
		if marker == "" {
			return nil
		}
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(line + "\n")
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	// Registered first: this checks Manager.Close after row cleanup, including
	// when Fatal exits the test before its body returns normally.
	t.Cleanup(func() {
		if m == nil {
			return
		}
		if err := m.pg.Ping(); err == nil {
			t.Error("manager did not close the database")
			return
		}
		if err := appendMarker("store closed"); err != nil {
			t.Error(err)
		}
	})
	var err error
	m, err = newTestManager(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	c, err := m.pg.CreateConversation("mainagent", "fixture cleanup owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan error, 1)
	if !s.goOwned(func() {
		<-s.ctx.Done()
		// A last owner write must finish while the row and store are still live.
		_, err := m.pg.Exec(`UPDATE conversations SET title=$1 WHERE id=$2`, "owner joined", c.ID)
		workerDone <- err
	}) {
		t.Fatal("fixture owner did not start")
	}
	cleanupTestManagerRows(t, m, func() error {
		if s.ctx.Err() == nil {
			return fmt.Errorf("row cleanup ran before server cancellation")
		}
		select {
		case err := <-workerDone:
			if err != nil {
				return fmt.Errorf("owner final write: %w", err)
			}
		default:
			return fmt.Errorf("row cleanup ran before owner joined")
		}
		if err := appendMarker("owner joined"); err != nil {
			return err
		}
		got, err := m.pg.GetConversation(c.ID)
		if err != nil || got == nil || got.Title != "owner joined" {
			return fmt.Errorf("owner final row missing: %+v %v", got, err)
		}
		if err := m.pg.DeleteConversation(c.ID); err != nil {
			return err
		}
		if got, err := m.pg.GetConversation(c.ID); err != nil || got != nil {
			return fmt.Errorf("fixture row survived cleanup: %+v %v", got, err)
		}
		return appendMarker("rows deleted")
	})
}
