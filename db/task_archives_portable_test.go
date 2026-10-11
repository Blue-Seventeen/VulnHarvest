package db

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func archiveContractDB(t *testing.T) *DB {
	t.Helper()
	var d *DB
	var err error
	if strings.Contains(t.Name(), "/SQLite") {
		d, err = OpenSQLite(filepath.Join(t.TempDir(), "archive.db"))
	} else {
		dsn := os.Getenv("ARTEX_ARCHIVE_TEST_PG_DSN")
		if dsn == "" {
			t.Fatal("ARTEX_ARCHIVE_TEST_PG_DSN required")
		}
		pool, e := sql.Open("pgx", dsn)
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
		d, err = Open(dsn)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func TestPortableArchiveEdgeContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("ConsistentReaderWriter", testArchiveConsistentReaderWriter)
			t.Run("SharedAndRemappedAssets", testArchiveSharedAndRemappedAssets)
			t.Run("ScopeProfilesSideContext", testArchiveScopeProfilesSideContext)
			t.Run("CountsCASDelete", testArchiveCountsCASDelete)
			t.Run("LegacyDefaults", testArchiveLegacyDefaults)
			t.Run("ColdDeleteDependencies", testArchiveColdDeleteDependencies)
			t.Run("StreamSequenceIntegrity", testArchiveStreamSequenceIntegrity)
		})
	}
}

func testArchiveStreamSequenceIntegrity(t *testing.T) {
	d := archiveContractDB(t)
	task, err := d.CreateTask("stream large", "exact bodies", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		if err = d.InsertLLMRecord(&LLMRecord{TaskID: strconv.FormatInt(task.ID, 10), RawRequest: strings.Repeat("raw request 中文\n", 1024), RawResponse: strings.Repeat("response "+strconv.Itoa(i), 2048)}); err != nil {
			t.Fatal(err)
		}
	}
	digest := func() ([32]byte, int64) {
		t.Helper()
		rows, err := d.Query(`SELECT id,raw_request,raw_response FROM llm_records WHERE task_id=$1 ORDER BY id`, strconv.FormatInt(task.ID, 10))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var body bytes.Buffer
		var max int64
		for rows.Next() {
			var request, response string
			var id int64
			if err = rows.Scan(&id, &request, &response); err != nil {
				t.Fatal(err)
			}
			body.WriteString(request)
			body.WriteByte(0)
			body.WriteString(response)
			body.WriteByte(0)
			if id > max {
				max = id
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(body.Bytes()), max
	}
	before, max := digest()
	job, snap, stream := archiveColdFixture(t, d, task)
	if snap.DataCounts["llm_records"] != 64 {
		t.Fatalf("stream count %v", snap.DataCounts)
	}
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	after, _ := digest()
	if before != after {
		t.Fatal("stream body SHA256 changed")
	}
	if err = d.InsertLLMRecord(&LLMRecord{TaskID: strconv.FormatInt(task.ID, 10), RawRequest: "next"}); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err = d.QueryRow(`SELECT id FROM llm_records WHERE raw_request='next'`).Scan(&next); err != nil || next <= max {
		t.Fatalf("sequence reused %d <= %d %v", next, max, err)
	}
}

func testArchiveColdDeleteDependencies(t *testing.T) {
	d := archiveContractDB(t)
	source, err := d.CreateTask("source", "delete", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.CreateTaskWithOptions("child", "dependency", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	compact := func(task *Task) *TaskArchive {
		t.Helper()
		if err := d.SetPaused(task.ID, true); err != nil {
			t.Fatal(err)
		}
		job, e := d.QueueTaskArchive(task.ID)
		if e != nil {
			t.Fatal(e)
		}
		claimed, e := d.ClaimTaskArchiveJob(t.Context())
		if e != nil || claimed.ID != job.ID {
			t.Fatalf("claim %+v %v", claimed, e)
		}
		snapshot, e := d.SnapshotTaskArchive(task.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = d.CompleteTaskArchive(job.ID, snapshot, filepath.Join(t.TempDir(), "owned.zst"), "test", 100, 50); e != nil {
			t.Fatal(e)
		}
		return job
	}
	childJob := compact(child)
	sourceJob := compact(source)
	if _, err = d.QueueTaskArchiveDelete(sourceJob.ID); !errors.Is(err, ErrTaskArchiveDeleteBlocked) {
		t.Fatalf("dependent cold package not protected %v", err)
	}
	for _, job := range []*TaskArchive{childJob, sourceJob} {
		if _, err = d.QueueTaskArchiveDelete(job.ID); err != nil {
			t.Fatal(err)
		}
		claimed, e := d.ClaimTaskArchiveJob(t.Context())
		if e != nil || claimed.ID != job.ID || claimed.State != Deleting {
			t.Fatalf("delete claim %+v %v", claimed, e)
		}
		if err = d.DeleteTaskArchiveStub(job.ID); err != nil {
			t.Fatal(err)
		}
		if archive, e := d.GetTaskArchive(job.ID); e != nil || archive != nil {
			t.Fatalf("delete left stub %+v %v", archive, e)
		}
	}
}

type archiveProbeWriter struct {
	bytes.Buffer
	probe  func() error
	called bool
}

func (w *archiveProbeWriter) Write(raw []byte) (int, error) {
	if !w.called {
		w.called = true
		if err := w.probe(); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(raw)
}

func testArchiveConsistentReaderWriter(t *testing.T) {
	d := archiveContractDB(t)
	task, err := d.CreateTask("consistent", "reader", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = d.InsertLLMRecord(&LLMRecord{TaskID: strconv.FormatInt(task.ID, 10), RawRequest: strings.Repeat("data", 10000)}); err != nil {
		t.Fatal(err)
	}
	if err = d.InsertLLMUsage(&LLMUsage{TaskID: strconv.FormatInt(task.ID, 10), InputTokens: 11}); err != nil {
		t.Fatal(err)
	}
	job, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	writer := &archiveProbeWriter{probe: func() error {
		done := make(chan error, 1)
		go func() {
			if e := d.UpdateTaskArchiveProgress(job.ID, "during_snapshot", 37); e != nil {
				done <- e
				return
			}
			_, e := d.Exec(`UPDATE llm_usage SET input_tokens=999 WHERE task_id=$1`, strconv.FormatInt(task.ID, 10))
			done <- e
		}()
		select {
		case e := <-done:
			return e
		case <-time.After(2 * time.Second):
			return errors.New("readonly snapshot blocked independent writer")
		}
	}}
	snapshot, err := d.SnapshotTaskArchiveWithLLMRecords(task.ID, writer)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodeArchiveRows(snapshot.Tables["llm_usage"])
	if err != nil {
		t.Fatal(err)
	}
	n, _ := jsonInt64(rows[0]["input_tokens"])
	if n != 11 {
		t.Fatalf("mixed snapshot input=%d", n)
	}
	stats, _ := json.Marshal(snapshot.AggregateStats)
	var parsed struct {
		Tokens struct {
			Input int64 `json:"input_tokens"`
		} `json:"tokens"`
	}
	if err = json.Unmarshal(stats, &parsed); err != nil || parsed.Tokens.Input != 11 {
		t.Fatalf("mixed aggregate %s %v", stats, err)
	}
	job, err = d.GetTaskArchive(job.ID)
	if err != nil || job.Progress != 37 {
		t.Fatalf("writer did not advance %+v %v", job, err)
	}
}

func archiveColdFixture(t *testing.T, d *DB, task *Task) (*TaskArchive, *TaskArchiveSnapshot, []byte) {
	t.Helper()
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	job, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	snap, err := d.SnapshotTaskArchiveWithLLMRecords(task.ID, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.CompleteTaskArchive(job.ID, snap, filepath.Join(t.TempDir(), "中文 包.zst"), "fixture", 100, 50); err != nil {
		t.Fatal(err)
	}
	if _, err = d.QueueTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = d.ClaimTaskArchiveJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return job, snap, stream.Bytes()
}

func testArchiveSharedAndRemappedAssets(t *testing.T) {
	d := archiveContractDB(t)
	task, err := d.CreateTask("archive assets", "restore", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTask("shared owner", "survives", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	own, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "exclusive.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	merge, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "merge.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "shared.example", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	node, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"asset_ids": []int64{own, merge, shared}}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{own, merge, shared} {
		if err = d.Exploration(task.ExplorationID).Anchor(node, id); err != nil {
			t.Fatal(err)
		}
	}
	otherNode, err := d.Exploration(other.ExplorationID).AddNode(KindFact, map[string]any{}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Exploration(other.ExplorationID).Anchor(otherNode, shared); err != nil {
		t.Fatal(err)
	}
	finding, err := d.AddFinding(task.ID, node, "TEST", "assets", SeverityLow, "summary", "evidence", "worker", []int64{own, merge, shared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE task_asset_links SET source='agent',source_summary='original provenance',source_node_id=$2 WHERE task_id=$1`, task.ID, node); err != nil {
		t.Fatal(err)
	}
	retest, _, _, err := d.CreateFindingRetest(t.Context(), finding, "retain snapshot references")
	if err != nil {
		t.Fatal(err)
	}
	job, snap, stream := archiveColdFixture(t, d, task)
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM assets WHERE id=$1`, shared).Scan(&count); err != nil || count != 1 {
		t.Fatalf("anchor-shared asset deleted %d %v", count, err)
	}
	if _, err = d.Exec(`INSERT INTO assets(id,type,domain,task_ids) VALUES($1,'root_domain','occupied.example',$2)`, own, d.archiveArray([]int64{other.ID})); err != nil {
		t.Fatal(err)
	}
	replacement, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "merge.example", TaskID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	var remapped int64
	if err = d.QueryRow(`SELECT id FROM assets WHERE domain='exclusive.example'`).Scan(&remapped); err != nil || remapped == own {
		t.Fatalf("ID conflict not remapped %d %v", remapped, err)
	}
	for _, id := range []int64{remapped, replacement, shared} {
		if err = d.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, node, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("anchor mapping %d: %v", id, err)
		}
	}
	var refs json.RawMessage
	if err = d.QueryRow(`SELECT asset_ids FROM findings WHERE id=$1`, finding).Scan(JSONScanner{&refs}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err = json.Unmarshal(refs, &ids); err != nil || len(ids) != 3 || ids[0] != remapped || ids[1] != replacement || ids[2] != shared {
		t.Fatalf("finding refs %s %v", refs, err)
	}
	if err = d.QueryRow(`SELECT snapshot FROM finding_retests WHERE id=$1`, retest.ID).Scan(JSONScanner{&refs}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(refs))
	decoder.UseNumber()
	var historical map[string]any
	if err = decoder.Decode(&historical); err != nil {
		t.Fatal(err)
	}
	references := historical["finding"].(map[string]any)["asset_ids"].([]any)
	mapped, _ := jsonInt64(references[0])
	if mapped != remapped {
		t.Fatalf("historical reference not mapped %d != %d", mapped, remapped)
	}
	var provenance string
	if err = d.QueryRow(`SELECT source_summary FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, replacement).Scan(&provenance); err != nil || provenance != "original provenance" {
		t.Fatalf("provenance %q %v", provenance, err)
	}
}

func testArchiveScopeProfilesSideContext(t *testing.T) {
	d := archiveContractDB(t)
	source, err := d.CreateTask("source", "retained", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var company, profile int64
	if err = d.QueryRow(`INSERT INTO companies(name,nkey) VALUES('company','archive-company') RETURNING id`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	if err = d.QueryRow(`INSERT INTO llm_profiles(name,format,model,api_key,base_url) VALUES('private model','openai','model','test-secret-API-key','https://fixture.invalid') RETURNING id`).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("scope context", "archive", TaskCreateOptions{CompanyIDs: []int64{company}, SourceTaskIDs: []int64{source.ID}, LLMProfileIDs: []int64{profile}})
	if err != nil {
		t.Fatal(err)
	}
	node, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"summary": "retain"}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := d.AddFinding(task.ID, node, "TEST", "side", SeverityHigh, "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, conv, _, err := d.CreateFindingRetest(t.Context(), finding, "conversation context")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"task-side", "conv-side"} {
		var args []any
		if session == "task-side" {
			args = []any{session, nil, task.ID, task.ExplorationID}
		} else {
			args = []any{session, conv.ID, nil, nil}
		}
		if _, err = d.Exec(`INSERT INTO side_question_sessions(session_key,conversation_id,task_id,exploration_id,run_id,version,snapshot,memory) VALUES($1,$2,$3,$4,1,2,'{"nested":[1,true,null]}','{"precise":9007199254740993}')`, args...); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`INSERT INTO side_question_requests(id,session_key,generation,client_id,question,answer,status,model,snapshot_at,usage,context_info) VALUES($1,$2,0,'client','question','原始 answer','completed','{"model":"fixture"}',$3,'{"tokens":9007199254740993}','{"messages":[{"role":"user","content":"上下文"}]}')`, session+"-request", session, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	job, snap, stream := archiveColdFixture(t, d, task)
	if snap.DataCounts["side_question_sessions"] != 2 || snap.DataCounts["side_question_requests"] != 2 {
		t.Fatalf("side contexts omitted %v", snap.DataCounts)
	}
	stats, _ := json.Marshal(snap.AggregateStats)
	if bytes.Contains(stats, []byte("test-secret")) {
		t.Fatal("API key leaked to public aggregate")
	}
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	var restoredSource, restoredProfile int64
	if err = d.QueryRow(`SELECT source_task_id FROM task_relations WHERE task_id=$1`, task.ID).Scan(&restoredSource); err != nil || restoredSource != source.ID {
		t.Fatal("source relation", err)
	}
	if err = d.QueryRow(`SELECT profile_id FROM task_llm_profiles WHERE task_id=$1`, task.ID).Scan(&restoredProfile); err != nil || restoredProfile != profile {
		t.Fatal("profile relation", err)
	}
	var key string
	if err = d.QueryRow(`SELECT api_key FROM llm_profiles WHERE id=$1`, profile).Scan(&key); err != nil || key != "test-secret-API-key" {
		t.Fatal("global model config changed", err)
	}
	var memory, usage, context json.RawMessage
	if err = d.QueryRow(`SELECT s.memory,r.usage,r.context_info FROM side_question_sessions s JOIN side_question_requests r ON r.session_key=s.session_key WHERE s.session_key='conv-side'`).Scan(JSONScanner{&memory}, JSONScanner{&usage}, JSONScanner{&context}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(memory, []byte("9007199254740993")) || !bytes.Contains(usage, []byte("9007199254740993")) || !bytes.Contains(context, []byte("上下文")) {
		t.Fatalf("context rounded/lost %s %s %s", memory, usage, context)
	}
	var ordinal int64
	if err = d.QueryRow(`SELECT MAX(ordinal) FROM side_question_requests`).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO side_question_requests(id,session_key,generation,client_id,question,status,model,snapshot_at) VALUES('after','task-side',0,'after','new','completed','{}',$1)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err = d.QueryRow(`SELECT ordinal FROM side_question_requests WHERE id='after'`).Scan(&next); err != nil || next <= ordinal {
		t.Fatalf("ordinal reused %d <= %d: %v", next, ordinal, err)
	}
}

func testArchiveCountsCASDelete(t *testing.T) {
	d := archiveContractDB(t)
	task, err := d.CreateTask("counts", "rollback", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.InsertLLMRecord(&LLMRecord{TaskID: strconv.FormatInt(task.ID, 10), RawRequest: "complete raw"}); err != nil {
		t.Fatal(err)
	}
	job, snap, stream := archiveColdFixture(t, d, task)
	snap.DataCounts["llm_records"]++
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err == nil {
		t.Fatal("stream count mismatch ignored")
	}
	if live, e := d.GetTask(task.ID); e != nil || live != nil {
		t.Fatalf("count failure committed task %+v %v", live, e)
	}
	snap.DataCounts["llm_records"]--
	snap.DataCounts["tasks"]++
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err == nil {
		t.Fatal("manifest count mismatch ignored")
	}
	snap.DataCounts["tasks"]--
	if err = d.FailTaskArchiveJob(job.ID, Archiving, errors.New("stale archive failure")); err != nil {
		t.Fatal(err)
	}
	current, err := d.GetTaskArchive(job.ID)
	if err != nil || current.State != Restoring {
		t.Fatalf("stale failure overwrote restore %+v %v", current, err)
	}
	if err = d.FailTaskArchiveJob(job.ID, Restoring, errors.New("retry")); err != nil {
		t.Fatal(err)
	}
	if _, err = d.QueueTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	if _, err = d.QueueTaskArchiveDelete(job.ID); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("restore allowed delete %v", err)
	}
	if err = d.CompleteTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
}

func testArchiveLegacyDefaults(t *testing.T) {
	d := archiveContractDB(t)
	task, err := d.CreateTask("legacy", "defaults", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO side_question_sessions(session_key,task_id,exploration_id,run_id,version,snapshot) VALUES('legacy',$1,$2,1,1,'{}')`, task.ID, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO side_question_requests(id,session_key,generation,client_id,question,status,model,snapshot_at) VALUES('legacy-request','legacy',0,'client','question','completed','{}',$1)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	job, snap, stream := archiveColdFixture(t, d, task)
	for _, pair := range [][2]string{{"side_question_sessions", "memory"}, {"side_question_requests", "context_info"}} {
		rows, e := decodeArchiveRows(snap.Tables[pair[0]])
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			delete(row, pair[1])
		}
		snap.Tables[pair[0]], err = json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"main_sessions", "finding_retests", "conversations", "conversation_activities", "task_intercept_rules"} {
		delete(snap.Tables, table)
		delete(snap.DataCounts, table)
	}
	if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	var memory, info string
	if err = d.QueryRow(`SELECT s.memory,r.context_info FROM side_question_sessions s JOIN side_question_requests r ON r.session_key=s.session_key WHERE s.session_key='legacy'`).Scan(&memory, &info); err != nil || memory != "{}" || info != "{}" {
		t.Fatalf("legacy defaults %q %q %v", memory, info, err)
	}
}

func TestPortableArchiveContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			d := archiveContractDB(t)
			task, err := d.CreateTaskWithOptions("中文 空格", "retain contexts", TaskCreateOptions{Name: "归档任务"})
			if err != nil {
				t.Fatal(err)
			}
			if err = d.SetPaused(task.ID, true); err != nil {
				t.Fatal(err)
			}
			asset, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "archive.example", TaskID: task.ID})
			if err != nil {
				t.Fatal(err)
			}
			node, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"asset_ids": []int64{asset}, "precise": int64(9007199254740993)}, 1, "confirmed", "worker", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = d.Exploration(task.ExplorationID).Anchor(node, asset); err != nil {
				t.Fatal(err)
			}
			finding, err := d.AddFinding(task.ID, node, "TEST", "保全复测", SeverityHigh, "summary", "evidence", "worker", []int64{asset})
			if err != nil {
				t.Fatal(err)
			}
			retest, conversation, _, err := d.CreateFindingRetest(t.Context(), finding, "保全会话上下文")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Exec(`INSERT INTO task_intercept_rules(task_id,kind,pattern) VALUES($1,'exact_domain','archive.example')`, task.ID); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertLLMRecord(&LLMRecord{SessionID: "conv-" + strconv.FormatInt(conversation.ID, 10), RawRequest: "复测 raw request", RawResponse: "复测 raw response"}); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertSkillUsage(&SkillUsage{Skill: "retest-skill", AgentKey: "retester", SessionID: "conv-" + strconv.FormatInt(conversation.ID, 10), Found: true}); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertToolUsage(&ToolUsage{ToolKey: "retest-tool", AgentKey: "retester", SessionID: "conv-" + strconv.FormatInt(conversation.ID, 10)}); err != nil {
				t.Fatal(err)
			}
			if _, err = d.Exec(`INSERT INTO main_sessions(exploration_id,seq) VALUES($1,42)`, task.ExplorationID); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertLLMUsage(&LLMUsage{TaskID: strconv.FormatInt(task.ID, 10), ExplorationID: task.ExplorationID, ProfileName: "模型 标签", InputTokens: 19, OutputTokens: 7}); err != nil {
				t.Fatal(err)
			}
			if err = d.InsertLLMRecord(&LLMRecord{TaskID: strconv.FormatInt(task.ID, 10), RawRequest: strings.Repeat("原始 request", 10000), RawResponse: strings.Repeat("response", 10000)}); err != nil {
				t.Fatal(err)
			}
			job, err := d.QueueTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			var stream bytes.Buffer
			snap, err := d.SnapshotTaskArchiveWithLLMRecords(task.ID, &stream)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(snap.Tables["exploration_nodes"], []byte("9007199254740993")) {
				t.Fatal("snapshot lost integer")
			}
			if snap.DataCounts["finding_retests"] != 1 || snap.DataCounts["conversations"] != 1 || snap.DataCounts["conversation_activities"] != 1 || snap.DataCounts["main_sessions"] != 1 || snap.DataCounts["task_intercept_rules"] != 1 || snap.DataCounts["llm_records"] != 2 {
				t.Fatal("snapshot omitted retest/session associations")
			}
			if snap.DataCounts["skill_usage"] != 1 || snap.DataCounts["tool_usage"] != 1 {
				t.Fatal("retest usage session association omitted")
			}
			taskRows, e := decodeArchiveRows(snap.Tables["tasks"])
			if e != nil {
				t.Fatal(e)
			}
			if taskRows[0]["paused"] != true || taskRows[0]["llm_profile_id"] != nil {
				t.Fatal("boolean/NULL snapshot types changed")
			}
			assetRows, e := decodeArchiveRows(snap.Tables["assets"])
			if e != nil {
				t.Fatal(e)
			}
			if _, ok := assetRows[0]["task_ids"].([]any); !ok {
				t.Fatal("asset IDs double encoded")
			}
			if err = d.CompleteTaskArchive(job.ID, snap, filepath.Join(t.TempDir(), "本次 archive.zst"), "test", 100, 50); err != nil {
				t.Fatal(err)
			}
			if _, err = d.QueueTaskArchiveRestore(job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = d.RestoreTaskArchiveWithLLMRecords(job.ID, snap, 0, bytes.NewReader(stream.Bytes())); err != nil {
				t.Fatal(err)
			}
			var rid, cid, seq int64
			if err = d.QueryRow(`SELECT id,conversation_id FROM finding_retests WHERE id=$1`, retest.ID).Scan(&rid, &cid); err != nil || cid != conversation.ID {
				t.Fatalf("retest association %d %v", cid, err)
			}
			if err = d.QueryRow(`SELECT seq FROM main_sessions WHERE exploration_id=$1`, task.ExplorationID).Scan(&seq); err != nil || seq != 42 {
				t.Fatalf("main session %d %v", seq, err)
			}
			var raw string
			if err = d.QueryRow(`SELECT raw_request FROM llm_records WHERE task_id=$1`, strconv.FormatInt(task.ID, 10)).Scan(&raw); err != nil || raw != strings.Repeat("原始 request", 10000) {
				t.Fatalf("raw restore %v", err)
			}
			if err = d.QueryRow(`SELECT raw_request FROM llm_records WHERE session_id=$1`, "conv-"+strconv.FormatInt(conversation.ID, 10)).Scan(&raw); err != nil || raw != "复测 raw request" {
				t.Fatalf("retest raw records %q %v", raw, err)
			}
			if err = d.CompleteTaskArchiveRestore(job.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
