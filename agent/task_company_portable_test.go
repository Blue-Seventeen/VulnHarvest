package agent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func closeTaskCompanyToolDB(t *testing.T, d *db.DB) {
	if !strings.HasPrefix(t.Name(), "TestPortableTaskCompanyTools/") {
		d.Close()
	}
}
func taskCompanyToolDB(t *testing.T) *db.DB {
	t.Helper()
	var d *db.DB
	var err error
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err = db.OpenSQLite(filepath.Join(t.TempDir(), "tools.db"))
	} else {
		dsn := os.Getenv("ARTEX_TASK_TEST_PG_DSN")
		if dsn == "" {
			t.Fatal("ARTEX_TASK_TEST_PG_DSN required")
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
		if name != "vh_task3" {
			t.Fatalf("refusing reset of %q", name)
		}
		if _, err := pool.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
			t.Fatal(err)
		}
		d, err = db.Open(dsn)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func TestPortableTaskCompanyTools(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("CompanyScope", TestGraphOverviewExpandsAssociatedCompanyScope)
			t.Run("DeletedTaskInsertion", testTaskToolDeletedTaskInsertion)
			t.Run("InsertItemRollback", testTaskToolInsertItemRollback)
			t.Run("LateWorkerBarrier", testTaskToolLateWorkerBarrier)
			t.Run("InsertItemSuccess", testTaskToolInsertItemSuccess)
			t.Run("ExistingMixedBatch", TestInsertAssetsMixedBatch)
		})
	}
}

func taskToolPeer(t *testing.T, d *db.DB) *db.DB {
	t.Helper()
	var peer *db.DB
	var err error
	if d.IsSQLite() {
		var seq int
		var name, path string
		if err := d.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		peer, err = db.OpenSQLite(path)
	} else {
		peer, err = db.Open(os.Getenv("ARTEX_TASK_TEST_PG_DSN"))
	}
	if err != nil {
		t.Fatal(err)
	}
	peer.DB.SetMaxOpenConns(1)
	peer.DB.SetMaxIdleConns(1)
	t.Cleanup(func() { peer.Close() })
	return peer
}

func testTaskToolLateWorkerBarrier(t *testing.T) {
	d := taskCompanyToolDB(t)
	_, task, intent := taskInsertToolFixture(t, d)
	peer := taskToolPeer(t, d)
	tools := NewToolSet(peer.Exploration(task.ExplorationID), "work#1")
	tools.SetTaskID(task.ID)
	tools.SetOwnerNode(intent)
	tools.SetAssetStore(peer.Assets(), peer.Companies())
	prepared := make(chan struct{})
	release := make(chan struct{})
	deleted := make(chan error, 1)
	go func() {
		_, err := d.DeleteTaskCascadePrepared(task.ID, true, false, false, func(db.TaskDeletePreparation) error { close(prepared); <-release; return nil })
		deleted <- err
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-prepared:
	case err := <-deleted:
		t.Fatal("delete did not prepare", err)
	case <-time.After(5 * time.Second):
		t.Fatal("prepare timeout")
	}
	pid := 0
	if peer.IsSQLite() {
		if _, err := peer.DB.Exec(`PRAGMA busy_timeout=100`); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		tx, err := peer.Begin()
		if err == nil {
			tx.Rollback()
			t.Fatal("native writer passed deletion barrier")
		}
		if !db.IsBusyError(err) || time.Since(started) < 70*time.Millisecond {
			t.Fatal("no native contention", err)
		}
		if _, err := peer.DB.Exec(`PRAGMA busy_timeout=5000`); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := peer.DB.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan map[string]any, 1)
	go func() {
		done <- callInsertAssets(t, tools, map[string]any{"assets": []any{map[string]any{"type": "subdomain", "domain": "late-worker.example.test", "record_type": "A", "record_value": []string{"192.0.2.8"}}}})
	}()
	deadline := time.Now().Add(3 * time.Second)
	observed := false
	for time.Now().Before(deadline) {
		select {
		case out := <-done:
			t.Fatal("handler passed deletion barrier", out)
		default:
		}
		if peer.IsSQLite() {
			observed = peer.DB.Stats().InUse == 1
		} else {
			if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&observed); err != nil {
				t.Fatal(err)
			}
		}
		if observed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !observed {
		t.Fatal("worker never leased writer / entered PG lock wait")
	}
	close(release)
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delete timeout")
	}
	var out map[string]any
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker timeout")
	}
	results, _ := out["results"].([]any)
	errs, _ := out["errors"].([]any)
	if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "task not found") {
		t.Fatal(out)
	}
	for _, table := range []string{"assets", "task_asset_links", "task_scope", "exploration_anchors"} {
		var n int
		if err := peer.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
}

func testTaskToolInsertItemSuccess(t *testing.T) {
	d := taskCompanyToolDB(t)
	tools, task, intent := taskInsertToolFixture(t, d)
	out := callInsertAssets(t, tools, map[string]any{"assets": []any{
		map[string]any{"type": "subdomain", "domain": "success.example.test", "record_type": "A", "record_value": []string{"192.0.2.8"}},
		map[string]any{"type": "ip", "ip": "invalid"},
		map[string]any{"type": "root_domain", "domain": "partial-success.test"},
	}})
	results, _ := out["results"].([]any)
	errs, _ := out["errors"].([]any)
	if len(results) != 2 || len(errs) != 1 {
		t.Fatal("partial success", out)
	}
	if tools.writes.Assets != 2 {
		t.Fatal(tools.writes)
	}
	assets, err := d.Assets().QueryByTask(task.ID, "", 20, 0)
	if err != nil || len(assets) != 4 {
		t.Fatal(assets, err)
	}
	explicit := map[string]bool{"success.example.test": true, "partial-success.test": true}
	for _, asset := range assets {
		if !explicit[asset.Domain] || asset.Type == "root_domain" && asset.Domain == "example.test" {
			continue
		}
		if asset.TaskSource != "agent" || asset.TaskSourceNodeID == nil || *asset.TaskSourceNodeID != intent {
			t.Fatal("provenance", asset)
		}
	}
	scopes, err := d.Assets().ListTaskScope(task.ID)
	if err != nil || len(scopes) != 2 || scopes[0].Kind != "subdomain" || scopes[0].Domain != "success.example.test" || scopes[1].Domain != "partial-success.test" {
		t.Fatal("derived scope expanded", scopes, err)
	}
	var anchors int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1`, intent).Scan(&anchors); err != nil || anchors != 2 {
		t.Fatal(anchors, err)
	}
	global := NewToolSet(nil, "")
	global.SetAssetStore(d.Assets(), d.Companies())
	out = callInsertAssets(t, global, map[string]any{"assets": []any{map[string]any{"type": "root_domain", "domain": "global-success.test"}}})
	results, _ = out["results"].([]any)
	errs, _ = out["errors"].([]any)
	if len(results) != 1 || len(errs) != 0 {
		t.Fatal("global insertion", out)
	}
}

func taskInsertToolFixture(t *testing.T, d *db.DB) (*ToolSet, *db.Task, int64) {
	t.Helper()
	task, err := d.CreateTask("worker insert", "atomic item", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"summary": "owner"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(d.Exploration(task.ExplorationID), "work#1")
	tools.SetTaskID(task.ID)
	tools.SetOwnerNode(intent)
	tools.SetAssetStore(d.Assets(), d.Companies())
	return tools, task, intent
}

func testTaskToolDeletedTaskInsertion(t *testing.T) {
	d := taskCompanyToolDB(t)
	tools, task, _ := taskInsertToolFixture(t, d)
	if err := d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	out := callInsertAssets(t, tools, map[string]any{"assets": []any{map[string]any{"type": "root_domain", "domain": "late-deleted.test"}}})
	results, _ := out["results"].([]any)
	errs, _ := out["errors"].([]any)
	if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "task not found") {
		t.Fatal("late worker accepted", out)
	}
	var assets int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE domain='late-deleted.test'`).Scan(&assets); err != nil || assets != 0 {
		t.Fatal(assets, err)
	}
}

func testTaskToolInsertItemRollback(t *testing.T) {
	for _, failure := range []string{"association", "scope", "anchor"} {
		t.Run(failure, func(t *testing.T) {
			d := taskCompanyToolDB(t)
			tools, task, _ := taskInsertToolFixture(t, d)
			table, predicate := "task_asset_links", "NEW.source='agent'"
			switch failure {
			case "scope":
				table, predicate = "task_scope", "NEW.domain='rollback-item.test'"
			case "anchor":
				table, predicate = "exploration_anchors", "TRUE"
			}
			if d.IsSQLite() {
				if _, err := d.Exec(fmt.Sprintf(`CREATE TRIGGER fail_item BEFORE INSERT ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'forced item failure'); END`, table, predicate)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION fail_item_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'forced item failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_item BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_item_fn()`, predicate, table)); err != nil {
					t.Fatal(err)
				}
			}
			out := callInsertAssets(t, tools, map[string]any{"assets": []any{map[string]any{"type": "root_domain", "domain": "rollback-item.test"}}})
			results, _ := out["results"].([]any)
			errs, _ := out["errors"].([]any)
			if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "forced item failure") {
				t.Fatal("persistence error ignored", out)
			}
			for _, query := range []string{`SELECT count(*) FROM assets WHERE domain='rollback-item.test'`, `SELECT count(*) FROM task_scope WHERE task_id=$1`, `SELECT count(*) FROM task_asset_links WHERE task_id=$1`, `SELECT count(*) FROM exploration_anchors ea JOIN exploration_nodes n ON n.id=ea.node_id WHERE n.exploration_id=$1`} {
				var count int
				var err error
				switch {
				case !strings.Contains(query, "$1"):
					err = d.QueryRow(query).Scan(&count)
				case strings.Contains(query, "exploration_id"):
					err = d.QueryRow(query, task.ExplorationID).Scan(&count)
				default:
					err = d.QueryRow(query, task.ID).Scan(&count)
				}
				if err != nil || count != 0 {
					t.Fatal("item did not rollback", query, count, err)
				}
			}
		})
	}
}
