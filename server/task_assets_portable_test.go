package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// These fixtures inject only storage; they never construct a runtime Manager,
// background Server, listener, Agent, model, notifier or MCP session.
func taskAssetServerDB(t *testing.T) *db.DB {
	t.Helper()
	var d *db.DB
	var err error
	if strings.Contains(t.Name(), "/SQLite/") {
		d, err = db.OpenSQLite(filepath.Join(t.TempDir(), "task-assets.db"))
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

func taskAssetServerFixture(t *testing.T, d *db.DB) (*Server, *Task) {
	t.Helper()
	stored, err := d.CreateTask("asset API", "seed atomic item", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: d, assets: d.Assets()}}, &Task{ID: strconv.FormatInt(stored.ID, 10), ExpID: stored.ExplorationID, Store: d.Exploration(stored.ExplorationID)}
}

func taskAssetHTTP(t *testing.T, s *Server, taskID, domain string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/assets", strings.NewReader(fmt.Sprintf(`{"task_id":%s,"assets":[{"type":"root_domain","domain":%q}]}`, taskID, domain)))
	w := httptest.NewRecorder()
	s.insertAssets(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPortableTaskAssetServer(t *testing.T) {
	for _, engine := range []string{"SQLite", "PostgreSQL"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("DeletedTask", testTaskAssetServerDeletedTask)
			t.Run("Rollback", testTaskAssetServerRollback)
			t.Run("Success", testTaskAssetServerSuccess)
			t.Run("PrepareBarrier", testTaskAssetServerPrepareBarrier)
		})
	}
}

func testTaskAssetServerDeletedTask(t *testing.T) {
	for _, path := range []string{"http", "seed"} {
		t.Run(path, func(t *testing.T) {
			d := taskAssetServerDB(t)
			s, task := taskAssetServerFixture(t, d)
			id, _ := strconv.ParseInt(task.ID, 10, 64)
			if err := d.DeleteTask(id); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			if path == "http" {
				out := taskAssetHTTP(t, s, task.ID, "server-late.test")
				results, _ := out["results"].([]any)
				errs, _ := out["errors"].([]any)
				if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "task not found") {
					t.Fatal(out)
				}
			} else {
				s.seed(task, "https://server-late.test")
				if !strings.Contains(logs.String(), "task not found") || strings.Contains(logs.String(), "目标站点") {
					t.Fatal("seed falsely reported success", logs.String())
				}
			}
			var n int
			if err := d.QueryRow(`SELECT count(*) FROM assets`).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func testTaskAssetServerRollback(t *testing.T) {
	for _, path := range []string{"http", "seed-source", "seed-anchor"} {
		t.Run(path, func(t *testing.T) {
			d := taskAssetServerDB(t)
			s, task := taskAssetServerFixture(t, d)
			table, predicate := "task_asset_links", "NEW.source IN ('api','task')"
			if path == "seed-anchor" {
				table, predicate = "exploration_anchors", "TRUE"
			}
			if d.IsSQLite() {
				if _, err := d.Exec(fmt.Sprintf(`CREATE TRIGGER fail_server_item BEFORE INSERT ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'forced server item failure'); END`, table, predicate)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION fail_server_item_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'forced server item failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_server_item BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_server_item_fn()`, predicate, table)); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			if path == "http" {
				out := taskAssetHTTP(t, s, task.ID, "server-rollback.test")
				results, _ := out["results"].([]any)
				errs, _ := out["errors"].([]any)
				if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "forced server item failure") {
					t.Fatal(out)
				}
			} else {
				s.seed(task, "https://server-rollback.test")
				if !strings.Contains(logs.String(), "forced server item failure") || strings.Contains(logs.String(), "目标站点") {
					t.Fatal(logs.String())
				}
			}
			for _, table := range []string{"assets", "task_asset_links", "task_scope", "exploration_anchors"} {
				var n int
				if err := d.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatal(table, n, err)
				}
			}
		})
	}
}

func testTaskAssetServerSuccess(t *testing.T) {
	d := taskAssetServerDB(t)
	s, task := taskAssetServerFixture(t, d)
	out := taskAssetHTTP(t, s, task.ID, "api-success.test")
	results, _ := out["results"].([]any)
	errs, _ := out["errors"].([]any)
	if len(results) != 1 || len(errs) != 0 {
		t.Fatal(out)
	}
	s.seed(task, "https://seed-success.test")
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	assets, err := d.Assets().QueryByTask(id, "", 10, 0)
	if err != nil || len(assets) != 3 {
		t.Fatal(assets, err)
	}
	var api, seed bool
	for _, asset := range assets {
		api = api || asset.TaskSource == "api"
		seed = seed || asset.TaskSource == "task"
	}
	if !api || !seed {
		t.Fatal("provenance missing", assets)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_anchors`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if scopes, err := d.Assets().ListTaskScope(id); err != nil || len(scopes) != 0 {
		t.Fatal("HTTP/seed auto scope changed", scopes, err)
	}
	out = taskAssetHTTP(t, s, "0", "global-api.test")
	results, _ = out["results"].([]any)
	errs, _ = out["errors"].([]any)
	if len(results) != 1 || len(errs) != 0 {
		t.Fatal(out)
	}
	w := httptest.NewRecorder()
	s.insertAssets(w, httptest.NewRequest("POST", "/api/assets", strings.NewReader(fmt.Sprintf(`{"task_id":%s,"assets":[{"type":"root_domain","domain":"first-partial.test"},{"type":"invalid"},{"type":"ip","ip":"192.0.2.77"}]}`, task.ID))))
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	results, _ = out["results"].([]any)
	errs, _ = out["errors"].([]any)
	if w.Code != 200 || len(results) != 2 || len(errs) != 1 || results[0].(map[string]any)["index"] != float64(0) || results[1].(map[string]any)["index"] != float64(2) {
		t.Fatal("HTTP partial result changed", w.Code, out)
	}
}

func testTaskAssetServerPrepareBarrier(t *testing.T) {
	for _, path := range []string{"http", "seed"} {
		t.Run(path, func(t *testing.T) {
			d := taskAssetServerDB(t)
			_, task := taskAssetServerFixture(t, d)
			var peer *db.DB
			var err error
			if d.IsSQLite() {
				var seq int
				var name, file string
				if err := d.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
					t.Fatal(err)
				}
				peer, err = db.OpenSQLite(file)
			} else {
				peer, err = db.Open(os.Getenv("ARTEX_TASK_TEST_PG_DSN"))
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { peer.Close() })
			peer.DB.SetMaxOpenConns(1)
			peer.DB.SetMaxIdleConns(1)
			s := &Server{m: &Manager{pg: peer, assets: peer.Assets()}}
			task.Store = peer.Exploration(task.ExpID)
			prepared := make(chan struct{})
			release := make(chan struct{})
			deleted := make(chan error, 1)
			id, _ := strconv.ParseInt(task.ID, 10, 64)
			go func() {
				_, err := d.DeleteTaskCascadePrepared(id, true, false, false, func(db.TaskDeletePreparation) error { close(prepared); <-release; return nil })
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
				t.Fatal(err)
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
					t.Fatal("native writer passed barrier")
				}
				if !db.IsBusyError(err) || time.Since(started) < 70*time.Millisecond {
					t.Fatal(err)
				}
				if _, err := peer.DB.Exec(`PRAGMA busy_timeout=5000`); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := peer.DB.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			done := make(chan map[string]any, 1)
			go func() {
				if path == "http" {
					done <- taskAssetHTTP(t, s, task.ID, "late-api.test")
				} else {
					s.seed(task, "https://late-seed.test")
					done <- nil
				}
			}()
			observed := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case out := <-done:
					t.Fatal("late call passed barrier", out)
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
				t.Fatal("late caller did not contend")
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
				t.Fatal("late caller timeout")
			}
			if path == "http" {
				results, _ := out["results"].([]any)
				errs, _ := out["errors"].([]any)
				if len(results) != 0 || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs), "task not found") {
					t.Fatal(out)
				}
			} else {
				if !strings.Contains(logs.String(), "task not found") || strings.Contains(logs.String(), "目标站点") {
					t.Fatal(logs.String())
				}
			}
			for _, table := range []string{"assets", "task_asset_links", "task_scope", "exploration_anchors"} {
				var n int
				if err := peer.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatal(table, n, err)
				}
			}
		})
	}
}
