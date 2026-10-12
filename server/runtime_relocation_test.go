package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func runtimeFixture(t *testing.T, base string) (*Manager, *Server) {
	t.Helper()
	m, e := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: filepath.Join(base, "data"), StateDir: filepath.Join(base, "state")})
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewWithOptions(context.Background(), m, filepath.Join(base, "skills"), m.dir, m.stateDir)
	if e != nil {
		m.Close()
		t.Fatal(e)
	}
	return m, s
}
func copyClosedRuntime(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		to := filepath.Join(destination, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0700)
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(to, raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeClosedFolderRelocation(t *testing.T) {
	old := t.TempDir()
	m, s := runtimeFixture(t, old)
	task, e := m.CreateTask("relocate", "raw mentions "+old, nil, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	m.SetTaskPaused(task.ID, true)
	file := filepath.Join(m.dir, "tasks", task.ID, "artifact.txt")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("portable-artifact"), 0600)
	n, _ := strconv.ParseInt(task.ID, 10, 64)
	job, e := m.pg.QueueTaskArchive(n)
	if e != nil {
		t.Fatal(e)
	}
	job, e = m.pg.ClaimTaskArchiveJobID(s.ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.runTaskArchiveJob(job); e != nil {
		t.Fatal(e)
	}
	cold, _ := m.pg.GetTaskArchive(job.ID)
	if filepath.IsAbs(cold.ArchivePath) || !strings.HasPrefix(cold.ArchivePath, "archives/tasks/") {
		t.Errorf("nonrelocatable package path: %q", cold.ArchivePath)
	}
	s.Shutdown(context.Background())
	m.Close()
	moved := t.TempDir()
	copyClosedRuntime(t, old, moved)
	os.RemoveAll(old)
	m, s = runtimeFixture(t, moved)
	defer m.Close()
	defer s.Shutdown(context.Background())
	if _, e = m.pg.QueueTaskArchiveRestore(job.ID); e != nil {
		t.Fatal(e)
	}
	job, e = m.pg.ClaimTaskArchiveJobID(s.ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.runTaskArchiveJob(job); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(m.dir, "tasks", task.ID, "artifact.txt"))
	if e != nil || string(raw) != "portable-artifact" {
		t.Fatalf("artifact %q %v", raw, e)
	}
	row, e := m.pg.GetTask(n)
	if e != nil || row.Goal != "raw mentions "+old {
		t.Fatalf("raw history rewritten: %+v %v", row, e)
	}
	// Re-archive, copy again, and permanently delete the cold package at new root.
	job, e = m.pg.QueueTaskArchive(n)
	if e != nil {
		t.Fatal(e)
	}
	job, e = m.pg.ClaimTaskArchiveJobID(s.ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.runTaskArchiveJob(job); e != nil {
		t.Fatal(e)
	}
	s.Shutdown(context.Background())
	m.Close()
	newer := t.TempDir()
	copyClosedRuntime(t, moved, newer)
	os.RemoveAll(moved)
	m, s = runtimeFixture(t, newer)
	defer m.Close()
	defer s.Shutdown(context.Background())
	if _, e = m.pg.QueueTaskArchiveDelete(job.ID); e != nil {
		t.Fatal(e)
	}
	job, e = m.pg.ClaimTaskArchiveJobID(s.ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.runTaskArchiveJob(job); e != nil {
		t.Fatal(e)
	}
	if row, e := m.pg.GetTaskArchive(job.ID); e != nil || row != nil {
		t.Fatalf("delete %+v %v", row, e)
	}
}

func TestRuntimeInterruptedJournalRelocation(t *testing.T) {
	old := t.TempDir()
	m, s := runtimeFixture(t, old)
	task, e := m.CreateTask("journal", "goal", nil, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	m.SetTaskPaused(task.ID, true)
	n, _ := strconv.ParseInt(task.ID, 10, 64)
	job, e := m.pg.QueueTaskArchive(n)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(m.dir, "tasks", task.ID, "artifact.txt")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("journal-preserved"), 0600)
	stage, e := stageTaskArchiveFilesWithSideRequests(m.dir, job.ID, task.ID, task.ExpID, nil, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(stage.root, "journal.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), old) {
		t.Errorf("absolute journal: %s", raw)
	}
	s.Shutdown(context.Background())
	m.Close()
	moved := t.TempDir()
	copyClosedRuntime(t, old, moved)
	os.RemoveAll(old)
	m, s = runtimeFixture(t, moved)
	defer m.Close()
	defer s.Shutdown(context.Background())
	raw, e = os.ReadFile(filepath.Join(m.dir, "tasks", task.ID, "artifact.txt"))
	if e != nil || string(raw) != "journal-preserved" {
		t.Fatalf("stage recovery %q %v", raw, e)
	}
	// Malicious journal source is rejected before touching an outside file.
	outside := t.TempDir()
	secret := filepath.Join(outside, "keep")
	os.WriteFile(secret, []byte("untouched"), 0600)
	journal := archiveStageJournal{ArchiveID: job.ID, TaskID: task.ID, Moves: []archiveFileMove{{Source: secret, Relative: "files/tasks/" + task.ID}}}
	root := filepath.Join(taskArchiveRoot(m.dir), ".staging", strconv.FormatInt(job.ID, 10))
	os.MkdirAll(root, 0700)
	raw, _ = json.Marshal(journal)
	os.WriteFile(filepath.Join(root, "journal.json"), raw, 0600)
	if e = recoverTaskArchiveStages(m.dir, m.pg); e == nil {
		t.Fatal("unsafe absolute journal accepted")
	}
	raw, _ = os.ReadFile(secret)
	if string(raw) != "untouched" {
		t.Fatal("outside changed")
	}
	_ = db.ArchiveQueued
}
