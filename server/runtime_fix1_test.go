package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeFix1SideSnapshotOwnership(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	for _, kind := range []string{"other-conversation", "absent-request", "invalid-id"} {
		t.Run(kind, func(t *testing.T) {
			data := t.TempDir()
			extracted := filepath.Join(data, "archives", "tasks", ".restore", "1-test")
			name := "exp0-btw-" + id + ".jsonl"
			snapshot := &db.TaskArchiveSnapshot{TaskID: 1, ExplorationID: 1, Tables: map[string]json.RawMessage{"conversations": json.RawMessage(`[{"id":7}]`), "side_question_requests": json.RawMessage(`[{"id":"` + id + `","session_key":"conv-8"}]`)}}
			if kind == "absent-request" {
				delete(snapshot.Tables, "side_question_requests")
			}
			if kind == "invalid-id" {
				name = "exp0-btw-user-file.jsonl"
			}
			source := filepath.Join(extracted, "files", "transcripts", name)
			os.MkdirAll(filepath.Dir(source), 0700)
			os.WriteFile(source, []byte("protected"), 0600)
			if _, err := installTaskArchiveFilesMode(data, extracted, "1", 1, true, snapshot); err == nil {
				t.Fatal("unowned side transcript accepted")
			}
			if raw, err := os.ReadFile(source); err != nil || string(raw) != "protected" {
				t.Fatal("rejected source mutated")
			}
			if _, err := os.Stat(filepath.Join(data, "transcripts", name)); !os.IsNotExist(err) {
				t.Fatal("rejected transcript installed")
			}
		})
	}
}

func TestRuntimeFix1JournalLateJunctionSwap(t *testing.T) {
	data, outside := t.TempDir(), t.TempDir()
	source := filepath.Join(data, "tasks", "1", "keep")
	destination := filepath.Join(data, "archives", "tasks", ".staging", "1", "payload", "files", "tasks", "1", "keep")
	os.MkdirAll(filepath.Dir(source), 0700)
	os.MkdirAll(filepath.Dir(destination), 0700)
	os.WriteFile(source, []byte("owned"), 0600)
	os.WriteFile(filepath.Join(outside, "keep"), []byte("external"), 0600)
	journal := archiveStageJournal{ArchiveID: 1, TaskID: "1", Moves: []archiveFileMove{{Source: "tasks/1", Relative: "files/tasks/1"}}}
	if err := resolvePortableStageJournal(data, &journal); err != nil {
		t.Fatal(err)
	}
	// Swap an ancestor after normalization. The later syscall must still refuse
	// following it outside the root, rather than relying on the prior check.
	os.Remove(source)
	os.Remove(filepath.Dir(source))
	runtimeLink(t, outside, filepath.Dir(source))
	if err := (archiveFileOps{base: data, confined: true}).Rename(source, destination); err == nil {
		t.Fatal("late junction escaped Root rename")
	}
	if raw, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(raw) != "external" {
		t.Fatal("external file mutated")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("external file imported")
	}
}

func TestRuntimeFix1MaliciousJournals(t *testing.T) {
	for _, kind := range []string{"restore-outside-source", "staging-traversal", "staging-task-mismatch", "staging-state-junction", "restore-state-junction", "relocated-legacy"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			m, s := runtimeFixture(t, base)
			defer m.Close()
			defer s.Shutdown(context.Background())
			task, e := m.CreateTask("malicious journal", "goal", nil, 0, 0)
			if e != nil {
				t.Fatal(e)
			}
			m.SetTaskPaused(task.ID, true)
			job, e := m.pg.QueueTaskArchive(numericTaskID(task.ID))
			if e != nil {
				t.Fatal(e)
			}
			outside := t.TempDir()
			keep := filepath.Join(outside, "keep.txt")
			os.WriteFile(keep, []byte("unchanged"), 0600)
			source := filepath.Join(m.dir, "tasks", task.ID)
			os.MkdirAll(source, 0700)
			os.WriteFile(filepath.Join(source, "owned.txt"), []byte("owned"), 0600)
			switch kind {
			case "restore-outside-source", "restore-state-junction":
				root := filepath.Join(taskArchiveRoot(m.dir), ".restore", itoa(job.ID)+"-test")
				os.MkdirAll(root, 0700)
				from := filepath.Join(outside, "stolen")
				if kind == "restore-state-junction" {
					from = filepath.Join(root, "files", "tasks", task.ID)
					os.MkdirAll(filepath.Join(root, "files"), 0700)
					runtimeLink(t, outside, filepath.Join(root, "files", "tasks"))
				}
				journal := archiveRestoreJournal{ArchiveID: job.ID, TaskID: numericTaskID(task.ID), Moves: []restoredArchivePath{{Source: from, Destination: source}}}
				raw, _ := json.Marshal(journal)
				os.WriteFile(filepath.Join(root, "restore-journal.json"), raw, 0600)
				if e = recoverTaskArchiveRestoreStages(m.dir, m.pg); e == nil {
					t.Fatal("malicious restore accepted")
				}
			default:
				root := filepath.Join(taskArchiveRoot(m.dir), ".staging", itoa(job.ID))
				os.MkdirAll(filepath.Join(root, "payload", "files", "tasks", task.ID), 0700)
				relative := "files/tasks/" + task.ID
				if kind == "staging-traversal" {
					relative = "../escape"
				}
				if kind == "staging-task-mismatch" {
					source = filepath.Join(m.dir, "tasks", "999")
				}
				if kind == "relocated-legacy" {
					source = filepath.Join(outside, "old-data", "tasks", task.ID)
				}
				if kind == "staging-state-junction" {
					os.RemoveAll(filepath.Join(m.dir, "tasks"))
					runtimeLink(t, outside, filepath.Join(m.dir, "tasks"))
				}
				journal := archiveStageJournal{ArchiveID: job.ID, TaskID: task.ID, Moves: []archiveFileMove{{Source: source, Relative: relative}}}
				raw, _ := json.Marshal(journal)
				os.WriteFile(filepath.Join(root, "journal.json"), raw, 0600)
				if e = recoverTaskArchiveStages(m.dir, m.pg); e == nil {
					t.Fatal("malicious staging accepted")
				}
			}
			raw, e := os.ReadFile(keep)
			if e != nil || string(raw) != "unchanged" {
				t.Fatalf("external file changed %q %v", raw, e)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 1 {
				t.Fatal("outside directory mutated")
			}
		})
	}
}

func TestRuntimeFix1RootOverlapRejectedBeforeStore(t *testing.T) {
	for _, kind := range []string{"data-base", "data-state", "data-under-state", "data-link-state", "skill-base", "skill-state", "skill-link-state"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			state := filepath.Join(base, "state")
			data := filepath.Join(base, "data")
			skills := filepath.Join(base, "skills")
			os.MkdirAll(state, 0700)
			switch kind {
			case "data-base":
				data = base
			case "data-state":
				data = state
			case "data-under-state":
				data = filepath.Join(state, "nested")
			case "data-link-state":
				data = filepath.Join(base, "data-link")
				runtimeLink(t, state, data)
			case "skill-base":
				skills = base
			case "skill-state":
				skills = state
			case "skill-link-state":
				skills = filepath.Join(base, "skill-link")
				runtimeLink(t, state, skills)
			}
			t.Setenv("ARTEX_SKILL_DIR", skills)
			m, e := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: state})
			if e == nil {
				m.Close()
				t.Fatal("overlapping browse/state root accepted")
			}
			if !strings.Contains(e.Error(), "overlap") {
				t.Fatalf("wrong rejection %v", e)
			}
			if _, e = os.Stat(filepath.Join(state, "main.sqlite")); !os.IsNotExist(e) {
				t.Fatal("database touched before overlap rejection")
			}
		})
	}
	base := t.TempDir()
	external := t.TempDir()
	data := filepath.Join(external, "data")
	skills := filepath.Join(external, "skills")
	t.Setenv("ARTEX_SKILL_DIR", skills)
	m, e := NewManagerWithOptions(ManagerOptions{Mode: "portable", DataDir: data, StateDir: filepath.Join(base, "state")})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	s, e := NewWithOptions(context.Background(), m, skills, data, m.stateDir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Shutdown(context.Background())
}
