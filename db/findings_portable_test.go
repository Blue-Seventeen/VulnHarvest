package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func findingContractDB(t *testing.T) *DB {
	t.Helper()
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err := OpenSQLite(filepath.Join(t.TempDir(), "findings.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := d.Close(); err != nil {
				t.Error(err)
			}
		})
		return d
	}
	dsn := os.Getenv("ARTEX_FINDING_TEST_PG_DSN")
	if dsn == "" {
		t.Fatal("ARTEX_FINDING_TEST_PG_DSN required")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var name string
	if err = pool.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "vh_findings" {
		t.Fatalf("refusing reset of %q", name)
	}
	if _, err = pool.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func openFindingTestDB(t *testing.T) (*DB, error) {
	if strings.HasPrefix(t.Name(), "TestPortableFindingContract/") {
		return findingContractDB(t), nil
	}
	return Open(testDSN(t))
}

func closeFindingTestDB(t *testing.T, d *DB) {
	if !strings.HasPrefix(t.Name(), "TestPortableFindingContract/") {
		d.Close()
	}
}

func TestPortableFindingContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("Delete", TestDeleteFinding)
			t.Run("PageStats", TestFindingsPageAndStats)
			t.Run("StablePage", TestListFindingsPageUsesStableIDTieBreaker)
			t.Run("Groups", TestFindingGroupsAndUnassignedPaging)
			t.Run("FollowUp", TestAddFindingFollowUpIntent)
			t.Run("AssetTree", TestBuildFindingAssetTree)
			t.Run("AssetScope", TestFindingAssetScopeFilter)
			t.Run("RetestDedupHistory", TestRetestAtomicDeduplicationAndHistory)
			t.Run("RetestDeleteRecover", TestRetestDeletionAndRestart)
			t.Run("RetestValidation", TestRetestValidationAndRollback)
			t.Run("RetestTriage", TestRetestFixedTriageOnlyAfterSuccessfulCompletion)
			t.Run("TrafficArchiveRows", testFindingTrafficArchiveRows)
			t.Run("AtomicPayloadMirror", testFindingAtomicPayloadMirror)
			t.Run("RetestSnapshotTypes", testFindingRetestSnapshotTypes)
			t.Run("ConcurrentTaskOperations", testFindingConcurrentTaskOperations)
			t.Run("ColdStats", testFindingColdStats)
			t.Run("Restart", testFindingRestart)
			t.Run("TaskDeleteBeforeFindingLock", testTaskDeleteBeforeFindingLock)
		})
	}
}

func testTaskDeleteBeforeFindingLock(t *testing.T) {
	for _, mode := range []string{"delete", "retain", "rollback"} {
		t.Run(mode, func(t *testing.T) { testTaskDeleteFindingLock(t, mode) })
	}
}

func testTaskDeleteFindingLock(t *testing.T, mode string) {
	d := findingContractDB(t)
	task, err := d.CreateTask("ordered concurrent deletion", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, Name: "deleted concurrently", Severity: "low"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	taskDone := make(chan error, 1)
	type deletionResult struct {
		n   int64
		err error
	}
	findingDone := make(chan deletionResult, 1)
	taskJoined, findingStarted, findingJoined := false, false, false
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		if !taskJoined {
			<-taskDone
		}
		if findingStarted && !findingJoined {
			<-findingDone
		}
	}()
	rollbackErr := errors.New("fixture rolls task deletion back")
	go func() {
		_, err := d.DeleteTaskCascadePrepared(task.ID, false, mode != "retain", false, func(TaskDeletePreparation) error {
			close(held)
			select {
			case <-release:
				if mode == "rollback" {
					return rollbackErr
				}
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		taskDone <- err
	}()
	select {
	case <-held:
	case err := <-taskDone:
		taskJoined = true
		t.Fatal("task deletion did not reach barrier", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	findingStarted = true
	go func() { n, err := d.DeleteFinding(f.FindingID); findingDone <- deletionResult{n, err} }()
	if !d.IsSQLite() {
		// Observe the real waiter after it read the old task_id, before task
		// deletion commits. This avoids hoping a scheduler hits the MVCC gap.
		for {
			var waiting bool
			err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			AND wait_event_type='Lock' AND query LIKE 'SELECT deleted_at FROM tasks WHERE id=%')`).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("finding never reached task row lock", ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	} else {
		select {
		case got := <-findingDone:
			findingJoined = true
			t.Fatalf("writer bypassed deletion: %+v", got)
		case <-time.After(25 * time.Millisecond):
		}
	}
	releaseOnce.Do(func() { close(release) })
	taskErr := <-taskDone
	taskJoined = true
	if (mode == "rollback" && !errors.Is(taskErr, rollbackErr)) || (mode != "rollback" && taskErr != nil) {
		t.Fatal(taskErr)
	}
	got := <-findingDone
	findingJoined = true
	want := int64(0)
	if mode != "delete" {
		want = 1
	}
	if got.err != nil || got.n != want {
		t.Fatalf("idempotent deletion after task commit: n=%d err=%v", got.n, got.err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM tasks WHERE id=$1`, task.ID).Scan(&count); err != nil || (mode == "rollback" && count != 1) || (mode != "rollback" && count != 0) {
		t.Fatalf("task transaction result: %d %v", count, err)
	}
}

func testFindingColdStats(t *testing.T) {
	d := findingContractDB(t)
	task, err := d.CreateTask("cold stats", "local fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(0, 0, "LIVE", "live", "low", "summary", "proof", "test", nil); err != nil {
		t.Fatal(err)
	}
	cold := taskArchiveAggregate{FindingStats: FindingStats{Total: 3, Pending: 2, Critical: 1, High: 2, VulnClasses: []string{"ARCHIVED", "LIVE"}}}
	raw, err := json.Marshal(cold)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_archives(task_id,state,aggregate_stats) VALUES($1,'ready',$2)`, task.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET archived_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := d.FindingStats()
	if err != nil || stats.Total != 4 || stats.Pending != 3 || stats.Critical != 1 || stats.High != 2 || stats.Low != 1 || len(stats.VulnClasses) != 2 {
		t.Fatalf("cold finding totals: %+v %v", stats, err)
	}
}

func testFindingRestart(t *testing.T) {
	d := findingContractDB(t)
	fid, err := d.AddFinding(0, 0, "RESTART", "persisted", "high", "summary", "proof", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, c, created, err := d.CreateFindingRetest(t.Context(), fid, "owned restart")
	if err != nil || !created {
		t.Fatal(r, c, err)
	}
	if ok, err := d.StartFindingRetest(t.Context(), r.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var path string
	if d.IsSQLite() {
		var seq int
		var name string
		if err := d.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var reopened *DB
	if path != "" {
		reopened, err = OpenSQLite(path)
	} else {
		reopened, err = Open(os.Getenv("ARTEX_FINDING_TEST_PG_DSN"))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	full, err := reopened.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil || full == nil || full.Status != "running" || !strings.Contains(string(full.Snapshot), "proof") {
		t.Fatalf("lost persisted retest: %+v %v", full, err)
	}
	if err := reopened.RecoverFindingRetests(); err != nil {
		t.Fatal(err)
	}
	full, err = reopened.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil || full.Status != "stopped" || full.FinishedAt == nil {
		t.Fatalf("recovery resumed work: %+v %v", full, err)
	}
}

func TestFindingArchiveVersionNormalization(t *testing.T) {
	raw, err := normalizeArchivedFindingVersions(json.RawMessage(`[{"id":9007199254740993,"report":"old"},{"id":2,"evidence_version":5,"report_evidence_version":-1}]`))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	if string(rows[0]["id"]) != "9007199254740993" || string(rows[0]["evidence_version"]) != "0" || string(rows[0]["report_evidence_version"]) != "0" || string(rows[1]["evidence_version"]) != "5" || string(rows[1]["report_evidence_version"]) != "-1" {
		t.Fatalf("archive normalization: %s", raw)
	}
}

func testFindingAtomicPayloadMirror(t *testing.T) {
	d := findingContractDB(t)
	task, err := d.CreateTask("payload mirror", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, Name: "original", VulnClass: "TEST", Severity: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := d.SetFindingName(f.FindingID, "renamed"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var raw json.RawMessage
	if err := d.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1`, f.NodeID).Scan(JSONScanner{&raw}); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload["name"] != "renamed" {
		t.Fatal(payload, err)
	}
	q := d.SQL(`CREATE FUNCTION reject_finding_payload() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.payload->>'name'='reject' THEN RAISE EXCEPTION 'fixture rejects mirror'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_finding_payload BEFORE UPDATE ON exploration_nodes FOR EACH ROW EXECUTE FUNCTION reject_finding_payload()`,
		`CREATE TRIGGER reject_finding_payload BEFORE UPDATE ON exploration_nodes WHEN json_extract(NEW.payload,'$.name')='reject' BEGIN SELECT RAISE(ABORT,'fixture rejects mirror'); END`)
	if _, err := d.Exec(q); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetFindingName(f.FindingID, "reject"); err == nil {
		t.Fatal("payload failure was ignored")
	}
	got, err := d.GetFinding(f.FindingID)
	if err != nil || got.Name != "renamed" {
		t.Fatalf("finding changed without payload: %+v %v", got, err)
	}
}

func testFindingRetestSnapshotTypes(t *testing.T) {
	d := findingContractDB(t)
	task, err := d.CreateTask("retest snapshot", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	const hugeID int64 = 9007199254740993
	extra := `{"huge":9007199254740993,"enabled":true,"nested":{"a":[1,2]}}`
	taskIDs := any([]int64{task.ID})
	auth := any([]string{`{"username":"fixture"}`})
	if d.IsSQLite() {
		raw, _ := json.Marshal([]int64{task.ID})
		taskIDs = string(raw)
		auth = `[{"username":"fixture"}]`
	}
	if _, err := d.Exec(`INSERT INTO assets(id,type,domain,task_ids,extra,auth) VALUES($1,'subdomain','snapshot.local',$2,$3,$4)`, hugeID, taskIDs, extra, auth); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_constraints(exploration_id,kind,text) VALUES($1,'deny','original constraint')`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	f, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, VulnClass: "TEST", Name: "snapshot", Severity: "low", Evidence: "original proof", AssetIDs: []int64{hugeID}})
	if err != nil {
		t.Fatal(err)
	}
	r, c, created, err := d.CreateFindingRetest(t.Context(), f.FindingID, "snapshot")
	if err != nil || !created {
		t.Fatal(r, c, created, err)
	}
	full, err := d.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(full.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	var assets []map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["assets"], &assets); err != nil || len(assets) != 1 {
		t.Fatal(assets, err)
	}
	if string(assets[0]["id"]) != "9007199254740993" || string(assets[0]["task_ids"])[0] != '[' || string(assets[0]["auth"])[0] != '[' {
		t.Fatalf("snapshot types/precision: %s", full.Snapshot)
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(assets[0]["extra"], &nested); err != nil || string(nested["huge"]) != "9007199254740993" || string(nested["enabled"]) != "true" {
		t.Fatal(nested, err)
	}
	var createdAt time.Time
	if err := json.Unmarshal(assets[0]["created_at"], &createdAt); err != nil || createdAt.IsZero() {
		t.Fatal(createdAt, err)
	}
	if _, err := d.Exec(`UPDATE assets SET extra='{}' WHERE id=$1`, hugeID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE task_constraints SET text='changed constraint' WHERE exploration_id=$1`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE findings SET evidence='changed proof' WHERE id=$1`, f.FindingID); err != nil {
		t.Fatal(err)
	}
	again, err := d.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil || string(again.Snapshot) != string(full.Snapshot) || strings.Contains(string(again.Snapshot), "changed") || !strings.Contains(string(again.Snapshot), "original constraint") {
		t.Fatalf("mutable retest snapshot: %s %v", again.Snapshot, err)
	}
}

func testFindingConcurrentTaskOperations(t *testing.T) {
	d := findingContractDB(t)
	task, err := d.CreateTask("concurrent findings", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, VulnClass: "TEST", Name: "existing", Severity: "low"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 4)
	wg.Go(func() {
		<-start
		_, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, VulnClass: "TEST", Name: "concurrent", Severity: "low"})
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrTaskArchiveState) {
			results <- err
		}
	})
	wg.Go(func() {
		<-start
		_, _, _, err := d.CreateFindingRetest(t.Context(), f.FindingID, "concurrent")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			results <- err
		}
	})
	wg.Go(func() { <-start; _, err := d.DeleteFinding(f.FindingID); results <- err })
	wg.Go(func() { <-start; _, err := d.DeleteTaskCascade(task.ID, false, true); results <- err })
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`SELECT count(*) FROM findings WHERE task_id=$1`, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, `SELECT count(*) FROM finding_retests WHERE finding_id=$1`} {
		id := task.ID
		if strings.Contains(q, "exploration_id") {
			id = task.ExplorationID
		}
		if strings.Contains(q, "finding_id") {
			id = f.FindingID
		}
		var count int
		if err := d.QueryRow(q, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial delete: %d %v %s", count, err, q)
		}
	}
}

func testFindingTrafficArchiveRows(t *testing.T) {
	d := findingContractDB(t)
	task, err := d.CreateTask("archive metadata fixture", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(task.ID, 0, "ARCHIVE", "binding", "low", "summary", "proof", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := TrafficEvidenceSnapshot{SourceTrafficID: "fixture", URL: "https://fixture.local/", Method: "GET", Status: 200, ContentType: "text/plain", ReqHash: strings.Repeat("a", 64), RespHash: strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	snap.ID = TrafficSnapshotID(snap)
	rawSnapshots, _ := json.Marshal([]TrafficEvidenceSnapshot{snap})
	rawBindings, _ := json.Marshal([]map[string]any{{"id": 9001, "finding_id": fid, "snapshot_id": snap.ID, "role": "proof", "note": "archive note", "position": 0, "created_at": snap.CreatedAt}})
	archive := &TaskArchiveSnapshot{FormatVersion: 3, TaskID: task.ID, Tables: map[string]json.RawMessage{"traffic_evidence_snapshots": rawSnapshots, "finding_traffic_bindings": rawBindings}}
	if err := d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error { return restoreFindingTrafficTx(tx, archive) }); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetFindingTraffic(t.Context(), fid)
	if err != nil || len(got.Bindings) != 1 || got.Bindings[0].ID != 9001 || got.Bindings[0].Note != "archive note" || got.Bindings[0].Snapshot.SourceTrafficID != "fixture" {
		t.Fatalf("archive binding: %+v %v", got, err)
	}
	archive.TaskID++
	if err := d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error { return restoreFindingTrafficTx(tx, archive) }); err == nil {
		t.Fatal("accepted cross-task archived binding")
	}
	archive.TaskID = task.ID
	archive.Tables["traffic_evidence_snapshots"], _ = json.Marshal([]TrafficEvidenceSnapshot{snap, snap})
	if err := d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error { return restoreFindingTrafficTx(tx, archive) }); err == nil {
		t.Fatal("accepted duplicate archived snapshot")
	}
}

func TestSQLiteConstraintClassification(t *testing.T) {
	d, err := OpenSQLite(filepath.Join(t.TempDir(), "errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`CREATE TABLE classifier_parent(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE classifier_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES classifier_parent(id) ON DELETE RESTRICT)`,
		`INSERT INTO classifier_parent VALUES(1)`,
		`INSERT INTO classifier_child VALUES(1,1)`,
		`CREATE TRIGGER classifier_user BEFORE INSERT ON classifier_parent WHEN NEW.id=3 BEGIN SELECT RAISE(ABORT,'user rule rejected'); END`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, query string
		fk          bool
	}{
		{"insert", `INSERT INTO classifier_child VALUES(2,99)`, true},
		{"restrict", `DELETE FROM classifier_parent WHERE id=1`, true},
		{"user trigger", `INSERT INTO classifier_parent VALUES(3)`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Exec(tc.query)
			if err == nil {
				t.Fatal("expected constraint violation")
			}
			t.Logf("native: %v", err)
			for _, e := range []error{err, fmt.Errorf("wrapped: %w", err)} {
				if IsForeignKeyViolation(e) != tc.fk {
					t.Fatalf("FK classification=%v want %v: %v", IsForeignKeyViolation(e), tc.fk, e)
				}
			}
		})
	}
}
