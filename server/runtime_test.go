package server

import (
	"context"
	"github.com/Autumn-27/artex/db"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimePortableHistoryPaused(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	data := filepath.Join(base, "data")
	os.MkdirAll(state, 0700)
	t.Setenv("ARTEX_PG_DSN", "this must never be parsed or connected")
	m, err := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.pg.CreateTask("preserve task", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.pg.SetSetting("portable-preserved", "value"); err != nil {
		t.Fatal(err)
	}
	m.Close()
	m, err = NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := NewWithOptions(context.Background(), m, filepath.Join(base, "skills"), data, state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	row, err := m.pg.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Paused || row.Queued || db.IsTerminal(row.Status) {
		t.Fatalf("history changed: %+v", row)
	}
	if v, _, _ := m.pg.GetSetting("portable-preserved"); v != "value" {
		t.Fatal("config lost")
	}
	if len(s.engine.runtimes) != 0 {
		t.Fatal("portable auto-started task runtime")
	}
	if _, err = os.Stat(filepath.Join(state, "main.sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(data, "main.sqlite")); !os.IsNotExist(err) {
		t.Fatal("main database inside workspace")
	}
}

func TestRuntimeManualArchiveDoesNotReplayHistory(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	data := filepath.Join(base, "data")
	m, err := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old, err := m.pg.CreateTask("old", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m.pg.SetPaused(old.ID, true)
	historical, err := m.pg.QueueTaskArchive(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWithOptions(context.Background(), m, filepath.Join(base, "skills"), data, state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	current, err := m.CreateTask("new", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m.SetTaskPaused(current.ID, true)
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("id", current.ID)
	w := httptest.NewRecorder()
	s.queueTaskArchive(w, r)
	if w.Code != 202 {
		t.Fatalf("manual archive: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		items, err := m.pg.ListTaskArchives("", "", 1, 50)
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, item := range items.Items {
			if item.TaskID != old.ID && item.State == db.ArchiveReady {
				ready = true
			}
		}
		if ready {
			row, _ := m.pg.GetTaskArchive(historical.ID)
			if row.State != db.ArchiveQueued {
				t.Fatalf("history replayed: %s", row.State)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("manual archive never executed")
}

func TestRuntimeShutdownDrainsLogWriter(t *testing.T) {
	d, err := db.OpenSQLite(filepath.Join(t.TempDir(), "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sink := &logSinkT{cap: 20, subs: map[chan LogLine]struct{}{}}
	sink.SetDB(context.Background(), d)
	sink.add(LogLine{Level: "info", Text: "last-before-close"})
	sink.CloseDB(d)
	logs, err := d.RecentLogs(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Text != "last-before-close" {
		t.Fatalf("not drained: %+v", logs)
	}
	sink.add(LogLine{Level: "info", Text: "after-close"})
	sink.SetDB(context.Background(), d)
	sink.add(LogLine{Level: "info", Text: "rebound"})
	sink.CloseDB(d)
	logs, err = d.RecentLogs(20)
	if err != nil || len(logs) != 2 {
		t.Fatalf("rebinding failed: %v %+v", err, logs)
	}
}
