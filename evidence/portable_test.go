package evidence

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func portableEvidenceDB(t *testing.T) *db.DB {
	t.Helper()
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err := db.OpenSQLite(filepath.Join(t.TempDir(), "evidence.db"))
		if err != nil {
			t.Fatal(err)
		}
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
	if err := pool.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "vh_findings" {
		t.Fatalf("refusing reset of %q", name)
	}
	if _, err := pool.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPortableEvidenceContract(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("Lifecycle", TestEvidenceBindingLifecycle)
			t.Run("AtomicFailures", TestEvidenceAtomicFailuresAndLegacy)
			t.Run("BindDelete", TestEvidenceConcurrentBindAndDelete)
			t.Run("GC", TestEvidenceGCGraceAndActiveRestore)
			t.Run("InstalledSameWriter", testInstalledSameWriter)
			t.Run("InvalidBody", testInvalidBody)
			t.Run("FailedNewBinding", testFailedNewBinding)
			t.Run("TaskOperations", testEvidenceTaskOperations)
		})
	}
}

func testFailedNewBinding(t *testing.T) {
	s, in, _ := evidenceFixture(t)
	seedExchange(t, s, "old", []byte("old body"), false)
	seedExchange(t, s, "new-a", []byte("new body a"), false)
	seedExchange(t, s, "new-b", []byte("new body b"), false)
	f, err := s.Record(t.Context(), in, []db.TrafficRef{{TrafficID: "old", Role: "proof"}})
	if err != nil {
		t.Fatal(err)
	}
	q := s.DB.SQL(`CREATE FUNCTION reject_evidence_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.role='verification' THEN RAISE EXCEPTION 'fixture rejects binding'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_evidence_binding BEFORE INSERT ON finding_traffic_bindings FOR EACH ROW EXECUTE FUNCTION reject_evidence_binding()`,
		`CREATE TRIGGER reject_evidence_binding BEFORE INSERT ON finding_traffic_bindings WHEN NEW.role='verification' BEGIN SELECT RAISE(ABORT,'fixture rejects binding'); END`)
	if _, err := s.DB.Exec(q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(t.Context(), f.FindingID, []db.TrafficRef{{TrafficID: "new-a", Role: "baseline"}, {TrafficID: "new-b", Role: "verification"}}); err == nil {
		t.Fatal("failed batch binding committed")
	}
	got, err := s.DB.GetFindingTraffic(t.Context(), f.FindingID)
	if err != nil || got.Version != f.Traffic.Version || len(got.Bindings) != 1 || got.Bindings[0].Snapshot.SourceTrafficID != "old" {
		t.Fatalf("partial binding/version: %+v %v", got, err)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM traffic_evidence_snapshots`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed batch left snapshot refs: %d %v", count, err)
	}
	if _, err := s.Traffic.DeleteHost("fixture.local"); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(t.Context(), time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	body, _, err := s.OpenBody(got.Bindings[0].Snapshot, "response")
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
}

func testEvidenceTaskOperations(t *testing.T) {
	s, in, _ := evidenceFixture(t)
	seedExchange(t, s, "task-race", []byte("race body"), false)
	f, err := s.Record(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 5)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_, err := s.Record(ctx, in, []db.TrafficRef{{TrafficID: "task-race"}})
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, db.ErrTaskArchiveState) {
			results <- err
		}
	})
	wg.Go(func() {
		<-start
		_, err := s.Bind(ctx, f.FindingID, []db.TrafficRef{{TrafficID: "task-race"}})
		if err != nil && !errors.Is(err, db.ErrFindingNotFound) {
			results <- err
		}
	})
	wg.Go(func() {
		<-start
		_, _, _, err := s.DB.CreateFindingRetest(ctx, f.FindingID, "race")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			results <- err
		}
	})
	wg.Go(func() {
		<-start
		_, err := s.DB.DeleteFinding(f.FindingID)
		results <- err
	})
	wg.Go(func() { <-start; _, err := s.DB.DeleteTaskCascade(in.TaskID, false, true); results <- err })
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, in.TaskID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, in.ExplorationID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM finding_traffic_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func testInstalledSameWriter(t *testing.T) {
	s, in, _ := evidenceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedExchange(t, s, "handoff", []byte("restored protected body"), false)
	f, err := s.Record(ctx, in, []db.TrafficRef{{TrafficID: "handoff"}})
	if err != nil {
		t.Fatal(err)
	}
	snap := f.Traffic.Bindings[0].Snapshot
	archive := t.TempDir()
	if err := s.CopySnapshots(ctx, []db.TrafficEvidenceSnapshot{snap}, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.DeleteFinding(f.FindingID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.Collect(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	installer, ok := any(s).(interface {
		WithInstalledSnapshotsTx(context.Context, []db.TrafficEvidenceSnapshot, string, func(*sql.Tx) error) error
	})
	if !ok {
		t.Fatal("missing same-writer evidence installation seam")
	}
	// GC uses a separately opened pool, proving the native DB barrier rather
	// than merely queueing behind this Store's one-connection writer pool.
	var peer *db.DB
	if s.DB.IsSQLite() {
		var seq int
		var name, path string
		if err := s.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		peer, err = db.OpenSQLite(path)
	} else {
		peer, err = db.Open(os.Getenv("ARTEX_FINDING_TEST_PG_DSN"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Error(err)
		}
	})
	collector := New(peer, s.Traffic, s.Dir)
	gcDone := make(chan error, 1)
	gcStarted, gcJoined := false, false
	var restoredID int64
	err = installer.WithInstalledSnapshotsTx(ctx, []db.TrafficEvidenceSnapshot{snap}, archive, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO findings(task_id,vulnclass,name,severity,summary,evidence,worker,asset_ids)
		VALUES($1,'RESTORE','restored','low','snapshot','proof','test','[]') RETURNING id`, in.TaskID).Scan(&restoredID); err != nil {
			return err
		}
		if err := db.AddFindingTrafficTx(tx, restoredID, []db.PreparedTrafficEvidence{{Ref: db.TrafficRef{Role: "proof"}, Snapshot: snap}}); err != nil {
			return err
		}
		gcStarted = true
		go func() { gcDone <- collector.Collect(ctx, now.Add(72*time.Hour)) }()
		select {
		case err := <-gcDone:
			gcJoined = true
			return errors.New("GC bypassed uncommitted metadata barrier: " + errorText(err))
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	})
	if gcStarted && !gcJoined {
		gcErr := <-gcDone
		if err == nil {
			err = gcErr
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.DB.GetFindingTraffic(ctx, restoredID)
	if err != nil || len(list.Bindings) != 1 {
		t.Fatalf("restored metadata: %+v %v", list, err)
	}
	body, _, err := s.OpenBody(list.Bindings[0].Snapshot, "response")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil || string(raw) != "restored protected body" {
		t.Fatalf("restored body=%q %v", raw, err)
	}
}

func errorText(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

func testInvalidBody(t *testing.T) {
	s, _, _ := evidenceFixture(t)
	for _, tc := range []struct {
		body   string
		length int64
		hash   string
	}{
		{"short", 20, ""}, {"valid length", 12, strings.Repeat("0", 64)},
	} {
		if _, err := s.writeBody(strings.NewReader(tc.body), tc.length, tc.hash); err == nil {
			t.Fatal("invalid body was published")
		}
	}
	// A non-directory target must still surface the real filesystem error.
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "blobs"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writeBody(strings.NewReader("body"), 4, ""); err == nil {
		t.Fatal("unwritable body destination ignored")
	}
}
