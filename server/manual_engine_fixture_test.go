package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestManualEngineRowCleanupJoinsDetachedWorker(t *testing.T) {
	testManualEngineRowCleanupOrder(t, "")
}

func TestManualEngineRowCleanupAfterFatal(t *testing.T) {
	if marker := os.Getenv("VH_MANUAL_ENGINE_FATAL_MARKER"); marker != "" {
		testManualEngineRowCleanupOrder(t, marker)
		t.Fatal("expected manual engine fixture fatal")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "manual-engine-cleanup.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestManualEngineRowCleanupAfterFatal$", "-test.v")
	// The child owns an independent SQLite file, never the PG parent's lock.
	child.Env = append(os.Environ(), "ARTEX_TEST_BACKEND=sqlite", "ARTEX_PG_DSN=", "VH_MANUAL_ENGINE_FATAL_MARKER="+marker)
	output, childErr := child.CombinedOutput()
	if childErr == nil || ctx.Err() != nil || !strings.Contains(string(output), "expected manual engine fixture fatal") {
		t.Fatalf("manual fatal child: err=%v output=%s", childErr, output)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "manual worker joined\nrows deleted\nstore closed\n" {
		t.Fatalf("manual fatal cleanup order=%q err=%v output=%s", got, err, output)
	}
}

func testManualEngineRowCleanupOrder(t *testing.T, marker string) {
	t.Helper()
	appendMarker := func(line string) error {
		if marker == "" {
			return nil
		}
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(line + "\n")
		return errors.Join(writeErr, f.Close())
	}
	var m *Manager
	t.Cleanup(func() {
		if m != nil {
			if err := m.pg.Ping(); err == nil {
				t.Error("manual fixture store did not close")
				return
			}
			if err := appendMarker("store closed"); err != nil {
				t.Error(err)
			}
		}
	})
	var err error
	m, err = newTestManager(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m)}
	if m.owner != nil {
		t.Fatal("manual fixture unexpectedly has a Server owner")
	}
	task, err := m.CreateTask("manual cleanup detached worker", "final write before deletion", nil, 0, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var findingID int64
	ready := false
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	rowCleanupStarted := make(chan struct{})
	workerWrite := make(chan error, 1)
	monitorResult := make(chan error, 1)
	var releaseOnce sync.Once
	releaseFinalWrite := func() { releaseOnce.Do(func() { close(release) }) }
	deleteRows := func() error {
		var findingErr error
		if findingID != 0 {
			_, findingErr = m.pg.DeleteFinding(findingID)
		}
		_, taskErr := m.DeleteTask(task.ID, DeleteTaskOptions{})
		return errors.Join(findingErr, taskErr)
	}
	cleanupManualTaskEngineRows(t, s, cancel, func() error {
		if !ready {
			return deleteRows()
		}
		close(rowCleanupStarted)
		if err := <-monitorResult; err != nil {
			// A RED run must report premature row admission, but still join and
			// clean its owned fixture before the Manager closes the store.
			s.engine.Shutdown()
			return errors.Join(err, deleteRows())
		}
		if err := <-workerWrite; err != nil {
			return fmt.Errorf("detached worker final write: %w", err)
		}
		if s.engine.inflightCount(task.ID) != 0 {
			return fmt.Errorf("row cleanup ran before detached runIntent returned")
		}
		finding, err := m.pg.GetFinding(findingID)
		if err != nil || finding == nil || finding.Summary != "manual worker joined" {
			return fmt.Errorf("final worker row missing: %+v %v", finding, err)
		}
		if err := appendMarker("manual worker joined"); err != nil {
			return err
		}
		if err := deleteRows(); err != nil {
			return err
		}
		if finding, err := m.pg.GetFinding(findingID); err != nil || finding != nil {
			return fmt.Errorf("finding survived cleanup: %+v %v", finding, err)
		}
		if row, err := m.pg.GetTask(mustTaskID(t, task.ID)); err != nil || row != nil {
			return fmt.Errorf("task survived cleanup: %+v %v", row, err)
		}
		return appendMarker("rows deleted")
	})
	// Setup failures must release a provider that may already have entered.
	// The ready path keeps the gate blocked until the monitor observes Shutdown.
	t.Cleanup(func() {
		if !ready {
			releaseFinalWrite()
		}
	})
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "cleanup join"}, 1, "paused", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	findingID, err = m.pg.AddFinding(0, 0, "test", "manual cleanup", "low", "pending", "fixture", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := agent.NewWorker(retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		result, err := m.pg.Exec(`UPDATE findings SET summary=$1 WHERE id=$2`, "manual worker joined", findingID)
		if err == nil {
			if n, rowsErr := result.RowsAffected(); rowsErr != nil || n != 1 {
				err = fmt.Errorf("final finding write rows=%d err=%v", n, rowsErr)
			}
		}
		workerWrite <- err
		return llm.Message{}, "", llm.Usage{}, ctx.Err()
	}}, "test", m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, worker })
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"message":"fixture final write","request_id":"manual-cleanup"}`))
	r.SetPathValue("id", task.ID)
	r.SetPathValue("iid", fmt.Sprint(iid))
	w := httptest.NewRecorder()
	s.sendWorkerMessage(w, r)
	if w.Code != 200 {
		t.Fatalf("start detached worker: status=%d body=%s", w.Code, w.Body)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("detached worker did not enter provider")
	}
	drain, stop := context.WithTimeout(context.Background(), time.Millisecond)
	err = s.waitTaskQuiescent(drain, task.ID)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked detached worker must fail bounded drain: %v", err)
	}
	go func() {
		defer releaseFinalWrite()
		<-cancelled
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(7 * time.Second)
		defer deadline.Stop()
		for {
			if s.engine.closing.Load() {
				select {
				case <-rowCleanupStarted:
					monitorResult <- fmt.Errorf("manual engine row cleanup entered before detached worker joined")
					return
				case <-time.After(20 * time.Millisecond):
				}
				if s.engine.inflightCount(task.ID) == 0 {
					monitorResult <- fmt.Errorf("blocked detached worker lost its inflight admission")
					return
				}
				monitorResult <- m.pg.Ping()
				return
			}
			select {
			case <-rowCleanupStarted:
				monitorResult <- fmt.Errorf("manual engine row cleanup entered before detached worker joined")
				return
			case <-ticker.C:
			case <-deadline.C:
				monitorResult <- fmt.Errorf("manual Engine shutdown was never admitted")
				return
			}
		}
	}()
	ready = true
}
