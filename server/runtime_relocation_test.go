package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/llm"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	t.Cleanup(func() { _ = m.Close() })
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
	// Real completed conversation-side row associated through the retest graph.
	finding, e := s.evidenceStore().Record(t.Context(), db.RecordFindingInput{TaskID: numericTaskID(task.ID), ExplorationID: task.ExpID, Name: "side fixture", VulnClass: "TEST", Severity: "low", Worker: "test"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	retest, conv, _, e := m.pg.CreateFindingRetest(t.Context(), finding.FindingID, "side")
	if e != nil {
		t.Fatal(e)
	}
	m.pg.FailPendingRetestForConversation(conv.ID, "fixture stopped")
	parent := sidequestion.Parent{ConversationID: conv.ID}
	snap := sidequestion.Snapshot{Parent: parent, RunID: 1, Version: 1, CapturedAt: time.Now(), Model: sidequestion.Model{WindowTokens: 200000}, Request: llm.CompletionRequest{Messages: []llm.Message{llm.UserText("main checkpoint")}}}
	if e = m.pg.SaveSideSnapshot(t.Context(), snap); e != nil {
		t.Fatal(e)
	}
	side, _, e := m.pg.StartSideRequest(t.Context(), snap, "fixture-request", "side question")
	if e != nil {
		t.Fatal(e)
	}
	provider := &sideHTTPProvider{started: make(chan llm.CompletionRequest, 4), release: make(chan struct{})}
	close(provider.release)
	runCtx, cancel := context.WithCancel(s.ctx)
	s.runSide(runCtx, cancel, *side, parent, provider, snap, "side question")
	completed, e := m.pg.SideRequest(t.Context(), side.ID)
	if e != nil || completed.Status != "completed" || completed.Answer == "" {
		t.Fatalf("side %+v %v", completed, e)
	}
	sideName := "exp0-btw-" + side.ID + ".jsonl"
	sideFile := filepath.Join(m.dir, "transcripts", sideName)
	os.MkdirAll(filepath.Dir(sideFile), 0700)
	sideRaw, _ := json.Marshal(completed)
	os.WriteFile(sideFile, sideRaw, 0600)
	_ = retest
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
	// Interrupt a real restore after file installation, before the DB commit.
	// Cold-copy startup must roll this relative journal back, then ordinary
	// restore below must still recover the completed side artifact.
	extracted := filepath.Join(taskArchiveRoot(m.dir), ".restore", itoa(job.ID)+"-interrupted")
	os.MkdirAll(extracted, 0700)
	packagePath, e := resolvePortableArchivePath(m.dir, cold)
	if e != nil {
		t.Fatal(e)
	}
	if e = extractTaskArchivePackage(packagePath, cold.SHA256, extracted); e != nil {
		t.Fatal(e)
	}
	manifest, e := os.ReadFile(filepath.Join(extracted, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var snapshot db.TaskArchiveSnapshot
	if e = json.Unmarshal(manifest, &snapshot); e != nil {
		t.Fatal(e)
	}
	if _, e = installTaskArchiveFilesMode(m.dir, extracted, task.ID, job.ID, true, &snapshot); e != nil {
		t.Fatal(e)
	}
	journal, e := os.ReadFile(filepath.Join(extracted, "restore-journal.json"))
	if e != nil || strings.Contains(string(journal), old) {
		t.Fatalf("absolute restore journal %s %v", journal, e)
	}
	s.Shutdown(context.Background())
	m.Close()
	moved := t.TempDir()
	copyClosedRuntime(t, old, moved)
	os.RemoveAll(old)
	m, s = runtimeFixture(t, moved)
	defer m.Close()
	defer s.Shutdown(context.Background())
	if _, e = os.Stat(filepath.Join(m.dir, "tasks", task.ID, "artifact.txt")); !os.IsNotExist(e) {
		t.Fatal("interrupted restore not rolled back after relocation")
	}
	if _, e = os.Stat(filepath.Join(taskArchiveRoot(m.dir), ".restore", itoa(job.ID)+"-interrupted")); !os.IsNotExist(e) {
		t.Fatal("restore journal not cleaned after rollback")
	}
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
	restoredSide, e := m.pg.SideRequest(t.Context(), side.ID)
	if e != nil || restoredSide == nil || restoredSide.Status != "completed" || restoredSide.Answer != completed.Answer {
		t.Fatalf("restored side %+v %v", restoredSide, e)
	}
	if raw, e := os.ReadFile(filepath.Join(m.dir, "transcripts", sideName)); e != nil || string(raw) != string(sideRaw) {
		t.Fatalf("side artifact %q %v", raw, e)
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

func numericTaskID(id string) int64 { n, _ := strconv.ParseInt(id, 10, 64); return n }
