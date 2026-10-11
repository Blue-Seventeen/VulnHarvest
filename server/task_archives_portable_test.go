package server

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidence"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/artex/traffic"
	"modernc.org/sqlite"
)

func archiveServerDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	var d *db.DB
	var err error
	path := filepath.Join(t.TempDir(), "server.db")
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err = db.OpenSQLite(path)
	} else {
		path = os.Getenv("ARTEX_ARCHIVE_TEST_PG_DSN")
		if path == "" {
			t.Fatal("ARTEX_ARCHIVE_TEST_PG_DSN required")
		}
		pool, e := sql.Open("pgx", path)
		if e != nil {
			t.Fatal(e)
		}
		var name string
		e = pool.QueryRow(`SELECT current_database()`).Scan(&name)
		if e != nil || name != "vh_archives" {
			pool.Close()
			t.Fatalf("refusing reset %q: %v", name, e)
		}
		_, e = pool.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`)
		pool.Close()
		if e != nil {
			t.Fatal(e)
		}
		d, err = db.Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	return d, path
}

func TestPortableArchiveServerContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			for _, scenario := range []string{"SuccessGC", "RowRollback", "CommitRollback", "FileCommitRetry"} {
				t.Run(scenario, testPortableArchiveServerRestore)
			}
			t.Run("ConversationAdmission", testPortableArchiveConversationAdmission)
		})
	}
}

func testPortableArchiveConversationAdmission(t *testing.T) {
	d, _ := archiveServerDB(t)
	dir := t.TempDir()
	m := &Manager{pg: d, dir: dir, tasks: map[string]*Task{}}
	s := &Server{ctx: t.Context(), m: m, engine: NewEngine(m), chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}, taskAgents: map[string]*taskAgentBundle{}}
	task, err := d.CreateTask("archive admission", "retest", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	f, err := s.evidenceStore().Record(t.Context(), db.RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Name: "admission", VulnClass: "TEST", Severity: "low", Worker: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, conv, _, err := d.CreateFindingRetest(t.Context(), f.FindingID, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	m.LoadExisting()
	job, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err = d.ClaimTaskArchiveJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Queued archives seal new retests even though the finding row still exists.
	if _, _, _, err = d.CreateFindingRetest(t.Context(), f.FindingID, "late"); !errors.Is(err, db.ErrTaskArchiveState) {
		t.Fatalf("late retest admitted: %v", err)
	}
	s.chatBusy[s.convBusyKey(conv.ID)] = true
	if err = s.archiveTask(job); err == nil {
		t.Fatal("active retest archived")
	}
	s.chatBusy[s.convBusyKey(conv.ID)] = false
	s.archiveConversationWrites = map[int64]int{conv.ID: 1}
	if err = s.archiveTask(job); err == nil {
		t.Fatal("active attachment write archived")
	}
	delete(s.archiveConversationWrites, conv.ID)
	s.side = &sideQuestionState{runs: map[string]sideRun{}, pending: map[string]sidequestion.Snapshot{}, latest: map[string]sidequestion.Snapshot{}, seen: map[string][2]int64{}}
	s.side.runs["active"] = sideRun{parent: sidequestion.Parent{ConversationID: conv.ID}}
	if err = s.archiveTask(job); err == nil {
		t.Fatal("active retest side archived")
	}
	delete(s.side.runs, "active")
	snapshot := sidequestion.Snapshot{Parent: sidequestion.Parent{ConversationID: conv.ID}, RunID: 3, Version: 7, CapturedAt: time.Now().UTC()}
	s.side.pending[snapshot.Parent.Key()] = snapshot
	if d.IsSQLite() {
		_, err = d.Exec(`CREATE TRIGGER fail_archive_context BEFORE INSERT ON side_question_sessions BEGIN SELECT RAISE(ABORT,'context persistence failure'); END`)
	} else {
		_, err = d.Exec(`CREATE FUNCTION fail_archive_context_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'context persistence failure'; END $$; CREATE TRIGGER fail_archive_context BEFORE INSERT ON side_question_sessions FOR EACH ROW EXECUTE FUNCTION fail_archive_context_fn()`)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = s.archiveTask(job); err == nil {
		t.Fatal("unflushed side context archived")
	}
	if len(s.archiveConversationPins) != 0 || len(s.side.pending) != 1 {
		t.Fatal("failed flush lost checkpoint or kept pin")
	}
	if _, err = d.Exec(`DROP TRIGGER fail_archive_context` + d.SQL(` ON side_question_sessions`, ``)); err != nil {
		t.Fatal(err)
	}
	ids, unpin, err := s.pinArchiveConversations(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unpin()
	if err = s.flushArchiveConversationContexts(ids); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = d.QueryRow(`SELECT version FROM side_question_sessions WHERE conversation_id=$1`, conv.ID).Scan(&version); err != nil || version != 7 {
		t.Fatalf("pending checkpoint not persisted %d %v", version, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /conv/{id}/send", s.pgSendConversationMessage)
	mux.HandleFunc("PATCH /conv/{id}", s.pgRenameConversation)
	mux.HandleFunc("PUT /conv/{id}", s.pgUpdateConversation)
	mux.HandleFunc("DELETE /conv/{id}", s.pgDeleteConversation)
	mux.HandleFunc("DELETE /batch", s.pgDeleteConversationsBatch)
	mux.HandleFunc("POST /upload", s.chatUpload)
	mux.HandleFunc("POST /side/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.handleSideQuestions(w, r, sidequestion.Parent{ConversationID: conv.ID})
	})
	mux.HandleFunc("DELETE /side/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.handleSideQuestions(w, r, sidequestion.Parent{ConversationID: conv.ID})
	})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	results := make(chan string, 7)
	for _, test := range []struct{ method, path, body string }{{"POST", "/conv/%d/send", `{"message":"late"}`}, {"PATCH", "/conv/%d", `{"title":"late"}`}, {"PUT", "/conv/%d", `{"llm_profile_id":null}`}, {"DELETE", "/conv/%d", ""}, {"DELETE", "/side/%d", ""}, {"POST", "/side/%d", `{"question":"late","client_request_id":"late"}`}, {"POST", "/upload?scope=session&id=conv-%d", ""}} {
		go func() {
			w := request(test.method, fmt.Sprintf(test.path, conv.ID), test.body)
			if w.Code != 409 {
				results <- fmt.Sprintf("pinned %s %s returned %d %s", test.method, test.path, w.Code, w.Body.String())
			} else {
				results <- ""
			}
		}()
	}
	for i := 0; i < 7; i++ {
		if result := <-results; result != "" {
			t.Error(result)
		}
	}
	w := request("DELETE", "/batch", fmt.Sprintf(`{"ids":[%d]}`, conv.ID))
	if w.Code != 409 {
		t.Fatalf("pinned batch %d", w.Code)
	}
	if _, err = os.Stat(filepath.Join(dir, "sessions", s.convBusyKey(conv.ID))); !os.IsNotExist(err) {
		t.Fatal("pinned upload created directory")
	}
	unpin()
	// The server's full archive flow now accepts the idle conversation, captures
	// its flushed context, and removes only the packaged hot conversation.
	if err = s.archiveTask(job); err != nil {
		t.Fatal("idle retry", err)
	}
	w = request("POST", fmt.Sprintf("/conv/%d/send", conv.ID), `{"message":"late"}`)
	if w.Code != 404 {
		t.Fatalf("compacted conversation send %d", w.Code)
	}
	// Explicitly prove stale loaded conversation data cannot bypass fresh DB check.
	s.archiveConversationMu.Lock()
	w = httptest.NewRecorder()
	ok := s.archiveConversationWritable(w, conv.ID)
	s.archiveConversationMu.Unlock()
	if ok || w.Code != 404 {
		t.Fatalf("stale conversation admitted %d", w.Code)
	}
}

func testPortableArchiveServerRestore(t *testing.T) {
	d, path := archiveServerDB(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tr, err := traffic.Open(filepath.Join(dir, "traffic"), ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	})
	m := &Manager{pg: d, dir: dir, traffic: tr, tasks: map[string]*Task{}}
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m)}
	task, err := d.CreateTask("真实 restore", "writer/evidence/files", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path) VALUES('proof',?,'fixture.local','POST','/test','https://fixture.local/test',200,'text/plain',2,23,'')`, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tr.DB().Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,resp_head,resp_body) VALUES('proof','POST /test',?,'HTTP 200',?)`, []byte("{}"), []byte("restored protected body"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.evidenceStore().Record(ctx, db.RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Name: "proof", VulnClass: "TEST", Severity: "low", Worker: "test"}, []db.TrafficRef{{TrafficID: "proof", Role: "proof"}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(ctx); err != nil {
		t.Fatal(err)
	}
	payload := t.TempDir()
	taskID := strconv.FormatInt(task.ID, 10)
	target := filepath.Join(dir, "tasks", taskID, "中文 空格.txt")
	source := filepath.Join(payload, "files", "tasks", taskID, "中文 空格.txt")
	if err = os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("保全文件"), 0600); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	snap, err := d.SnapshotTaskArchiveWithLLMRecords(task.ID, &stream)
	if err != nil {
		t.Fatal(err)
	}
	streamPath := filepath.Join(payload, filepath.FromSlash(db.TaskArchiveLLMRecordsPath))
	if err = os.MkdirAll(filepath.Dir(streamPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(streamPath, stream.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	snapshots, err := db.ArchiveEvidenceSnapshots(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.evidenceStore().CopySnapshots(ctx, snapshots, filepath.Join(payload, "evidence")); err != nil {
		t.Fatal(err)
	}
	archivePath := taskArchivePath(dir, job.ID, taskID)
	original, compressed, checksum, err := writeTaskArchivePackage(archivePath, payload, snap)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.CompleteTaskArchive(job.ID, snap, archivePath, checksum, original, compressed); err != nil {
		t.Fatal(err)
	}
	// Recreate bodies and snapshot metadata from this package, not a surviving hot copy.
	if _, err = d.Exec(`DELETE FROM traffic_evidence_snapshots WHERE NOT EXISTS(SELECT 1 FROM finding_traffic_bindings b WHERE b.snapshot_id=traffic_evidence_snapshots.id)`); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(dir, "evidence", "blobs")); err != nil {
		t.Fatal(err)
	}
	if _, err = d.QueueTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = d.ClaimTaskArchiveJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var peer *db.DB
	if d.IsSQLite() {
		peer, err = db.OpenSQLite(path)
	} else {
		peer, err = db.Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Error(err)
		}
	})
	// A real INSERT trigger stops the server inside its owned restore transaction.
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	scenario := t.Name()[strings.LastIndex(t.Name(), "/")+1:]
	var held *os.File
	barrier := func() error {
		if scenario == "FileCommitRetry" {
			matches, e := filepath.Glob(filepath.Join(taskArchiveRoot(dir), ".restore", "*", "restore-journal.json"))
			if e != nil || len(matches) != 1 {
				return fmt.Errorf("restore journal %v %v", matches, e)
			}
			held, e = os.Open(matches[0])
			if e != nil {
				return e
			}
		}
		close(entered)
		<-release
		if scenario == "RowRollback" {
			return errors.New("injected restore row failure")
		}
		return nil
	}
	if d.IsSQLite() {
		name := "archive_barrier_" + strings.ToLower(scenario) + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err = sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) { return int64(1), barrier() }); err != nil {
			t.Fatal(err)
		}
		d.DB.SetMaxIdleConns(0)
		d.DB.SetMaxIdleConns(1)
		if _, err = d.Exec(`CREATE TRIGGER archive_barrier BEFORE INSERT ON findings BEGIN SELECT ` + name + `(); END`); err != nil {
			t.Fatal(err)
		}
	} else {
		// PostgreSQL blocks at a session lock until the test releases it. A separate
		// watcher signals the same point by observing pg_stat_activity's lock wait.
		probe, e := peer.DB.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer probe.Close()
		const key = int64(7337741099)
		if _, e = probe.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
			t.Fatal(e)
		}
		defer probe.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		failure := ""
		if scenario == "RowRollback" {
			failure = "RAISE EXCEPTION 'injected restore row failure';"
		}
		if _, err = d.Exec(`CREATE FUNCTION archive_barrier_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(7337741099); ` + failure + ` RETURN NEW; END $$; CREATE TRIGGER archive_barrier BEFORE INSERT ON findings FOR EACH ROW EXECUTE FUNCTION archive_barrier_fn()`); err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				var waiting bool
				e := peer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname='vh_archives' AND wait_event='advisory' AND query LIKE 'INSERT INTO findings%')`).Scan(&waiting)
				if e != nil {
					return
				}
				if waiting {
					_ = barrier()
					_, _ = probe.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, key)
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		}()
	}
	if scenario == "CommitRollback" {
		if _, err = d.Exec(`CREATE TABLE archive_commit_failure(id INTEGER REFERENCES tasks(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
			t.Fatal(err)
		}
		if d.IsSQLite() {
			_, err = d.Exec(`CREATE TRIGGER archive_commit_failure AFTER INSERT ON findings BEGIN INSERT INTO archive_commit_failure VALUES(-999); END`)
		} else {
			_, err = d.Exec(`CREATE FUNCTION archive_commit_failure_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO archive_commit_failure VALUES(-999); RETURN NEW; END $$; CREATE TRIGGER archive_commit_failure AFTER INSERT ON findings FOR EACH ROW EXECUTE FUNCTION archive_commit_failure_fn()`)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.restoreTaskArchive(job) }()
	select {
	case <-entered:
	case e := <-done:
		t.Fatalf("restore did not reach metadata barrier: %v", e)
	case <-ctx.Done():
		t.Fatal("restore stalled before actual metadata write")
	}
	collector := evidence.New(peer, tr, filepath.Join(dir, "evidence"))
	gc := make(chan error, 1)
	go func() { gc <- collector.Collect(ctx, time.Now().Add(72*time.Hour)) }()
	select {
	case err := <-gc:
		t.Fatalf("GC escaped uncommitted restore: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	released = true
	err = <-done
	gcErr := <-gc
	if gcErr != nil {
		t.Fatal(gcErr)
	}
	if held != nil {
		defer held.Close()
	}
	if scenario == "RowRollback" || scenario == "CommitRollback" {
		if err == nil {
			t.Fatal("restore failure was swallowed")
		}
		if live, e := d.GetTask(task.ID); e != nil || live != nil {
			t.Fatalf("failed DB restore leaked task: %+v %v", live, e)
		}
		if _, e := os.Stat(target); !os.IsNotExist(e) {
			t.Fatalf("failed DB restore leaked file: %v", e)
		}
		if _, e := os.Stat(archivePath); e != nil {
			t.Fatal("failed restore consumed package", e)
		}
		if _, e := d.Exec(`DROP TRIGGER archive_barrier` + d.SQL(` ON findings`, ``)); e != nil {
			t.Fatal(e)
		}
		if scenario == "CommitRollback" {
			if _, e := d.Exec(`DROP TRIGGER archive_commit_failure` + d.SQL(` ON findings`, ``)); e != nil {
				t.Fatal(e)
			}
		}
		if err = s.restoreTaskArchive(job); err != nil {
			t.Fatal("retry", err)
		}
	} else if scenario == "FileCommitRetry" {
		// Windows rejects cleanup while the journal is open. DB/files remain committed;
		// retry resolves the journal and consumes the package without duplicate rows.
		if err == nil && runtime.GOOS == "windows" {
			t.Fatal("expected held-file cleanup failure on Windows")
		}
		if live, e := d.GetTask(task.ID); e != nil || live == nil {
			t.Fatalf("cleanup failure rolled back committed task %v", e)
		}
		cleanupFailed := err != nil
		held.Close()
		held = nil
		if cleanupFailed {
			if err = s.restoreTaskArchive(job); err != nil {
				t.Fatal("cleanup retry", err)
			}
		}
	} else if err != nil {
		t.Fatal(err)
	}
	raw, e := os.ReadFile(target)
	if e != nil || string(raw) != "保全文件" {
		t.Fatalf("restored file %q %v", raw, e)
	}
	list, e := d.GetFindingTraffic(ctx, f.FindingID)
	if e != nil || len(list.Bindings) != 1 {
		t.Fatalf("evidence metadata %+v %v", list, e)
	}
	body, _, e := s.evidenceStore().OpenBody(list.Bindings[0].Snapshot, "response")
	if e != nil {
		t.Fatal(e)
	}
	raw, e = io.ReadAll(body)
	body.Close()
	if e != nil || string(raw) != "restored protected body" {
		t.Fatalf("body %q %v", raw, e)
	}
	var count int
	if e = d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate restore %d %v", count, e)
	}
}
