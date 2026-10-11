package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sqliteTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenSQLite(filepath.Join(t.TempDir(), "状态 空格.sqlite"))
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

func TestSQLiteSchemaAndTypes(t *testing.T) {
	d := sqliteTestDB(t)
	if !d.IsSQLite() || d.SQL("postgres", "sqlite") != "sqlite" || (&DB{}).SQL("postgres", "sqlite") != "postgres" {
		t.Fatal("dialect selection")
	}
	for _, table := range []string{"agents", "llm_records", "llm_usage", "side_question_requests", "notification_deliveries", "task_archives"} {
		var n int
		if err := d.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(table, err)
		}
	}
	var id int64
	var created time.Time
	var enabled bool
	var arrays string
	if err := d.QueryRow(`INSERT INTO assets(type,ip) VALUES('ip','192.0.2.1') RETURNING id,created_at,task_ids`).Scan(&id, &created, &arrays); err != nil {
		t.Fatal(err)
	}
	if created.IsZero() || arrays != "[]" {
		t.Fatalf("time/JSON defaults: %v %q", created, arrays)
	}
	if err := d.QueryRow(`INSERT INTO asset_intercept_rules(kind,pattern) VALUES('exact_ip','192.0.2.1') RETURNING enabled`).Scan(&enabled); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	if _, err := d.Exec(`UPDATE assets SET ip='192.0.2.2',updated_at='2000-01-01' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var updated time.Time
	if err := d.QueryRow(`SELECT updated_at FROM assets WHERE id=$1`, id).Scan(&updated); err != nil || updated.Year() < 2020 {
		t.Fatal(updated, err)
	}
	var a, b, c string
	if err := d.QueryRow(`SELECT $2,$1,$2`, "a", "b").Scan(&a, &b, &c); err != nil || a != "b" || b != "a" || c != "b" {
		t.Fatal("numbered parameters", a, b, c, err)
	}
	if err := d.QueryRow(`SELECT concat_ws('-',NULL,'a',2),greatest(NULL,2,9),least(NULL,2,9)`).Scan(&a, &b, &c); err != nil || a != "a-2" || b != "9" || c != "2" {
		t.Fatal("scalar functions", a, b, c, err)
	}
}

func TestSQLitePoolsTransactionsAndErrors(t *testing.T) {
	d := sqliteTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conns := []*sql.Conn{}
	for i := 0; i < 4; i++ {
		c, err := d.reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		for _, p := range []struct {
			q    string
			want int
		}{{"PRAGMA foreign_keys", 1}, {"PRAGMA busy_timeout", 5000}, {"PRAGMA query_only", 1}} {
			var v int
			if err := c.QueryRowContext(ctx, p.q).Scan(&v); err != nil || v != p.want {
				t.Fatal(p.q, v, err)
			}
		}
		c.Close()
	}
	if d.DB.Stats().MaxOpenConnections != 1 {
		t.Fatal("writer must be serialized")
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO companies(name,nkey) VALUES('rollback','rollback')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM companies`).Scan(&n); err != nil || n != 0 {
		t.Fatal("reader blocked or dirty", n, err)
	}
	var a, b string
	if err := tx.QueryRow(`SELECT $2,$1`, "a", "b").Scan(&a, &b); err != nil || a != "b" || b != "a" {
		t.Fatal(a, b, err)
	}
	tx.Rollback()
	if _, err := d.ExecContext(ctx, `INSERT INTO companies(name,nkey) VALUES('one','one')`); err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(`INSERT INTO companies(name,nkey) VALUES('duplicate','one')`)
	if !IsUniqueViolation(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("unique classification", err)
	}
	_, err = d.Exec(`INSERT INTO assets(type,ip,company_id) VALUES('ip','192.0.2.3',999)`)
	if !IsForeignKeyViolation(err) {
		t.Fatal("FK classification", err)
	}
	ro, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Exec(`DELETE FROM companies`); err == nil {
		t.Fatal("read-only tx accepted mutation")
	}
	ro.Rollback()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := d.Exec(`INSERT INTO companies(name,nkey) VALUES($1,$1)`, fmt.Sprint(i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteRestartBackupFutureAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO settings(key,value) VALUES('custom','preserve')`)
	d.Exec(`PRAGMA user_version=0`)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='custom'`).Scan(&value); err != nil || value != "preserve" {
		t.Fatal(value, err)
	}
	files, _ := filepath.Glob(path + ".backup-v0-*")
	if len(files) != 1 {
		t.Fatal("migration backup missing", files)
	}
	backup, err := sql.Open("sqlite", files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT value FROM settings WHERE key='custom'`).Scan(&value); err != nil || value != "preserve" {
		t.Fatal("backup not recoverable", value, err)
	}
	backup.Close()
	d.Exec(`PRAGMA user_version=999`)
	d.Close()
	if next, err := OpenSQLite(path); err == nil {
		next.Close()
		t.Fatal("future schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec(`PRAGMA user_version=0`)
	if err := migrateSQLite(raw, path, []string{`CREATE TABLE should_rollback(id INTEGER); INVALID SQL`}); err == nil {
		t.Fatal("invalid migration accepted")
	}
	var n int
	raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='should_rollback'`).Scan(&n)
	if n != 0 {
		t.Fatal("failed migration did not roll back")
	}
	raw.Close()
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal("failed open leaked handle", err)
	}
	if _, err := OpenSQLite(""); err == nil {
		t.Fatal("empty path accepted")
	}
	if IsBusyError(errors.New("not sqlite")) {
		t.Fatal("misclassified generic error")
	}
}

func TestSQLiteFinalPostgresShape(t *testing.T) {
	data, err := os.ReadFile("sqlite_schema_fixture_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Columns []struct {
			Table    string `json:"table_name"`
			Name     string `json:"column_name"`
			Nullable string `json:"is_nullable"`
			DataType string `json:"data_type"`
		} `json:"columns"`
		Indexes []string `json:"indexes"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	d := sqliteTestDB(t)
	byTable := map[string]map[string]int{}
	columnTypes := map[string]string{}
	for _, c := range meta.Columns {
		if byTable[c.Table] == nil {
			rows, err := d.Query(`PRAGMA table_info("` + c.Table + `")`)
			if err != nil {
				t.Fatal(err)
			}
			cols := map[string]int{}
			for rows.Next() {
				var cid, nn, pk int
				var name, typ string
				var def sql.NullString
				if err := rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
					t.Fatal(err)
				}
				if pk != 0 {
					nn = 1
				}
				cols[name] = nn
				columnTypes[c.Table+"."+name] = typ
			}
			rows.Close()
			byTable[c.Table] = cols
		}
		nn, ok := byTable[c.Table][c.Name]
		if !ok {
			t.Errorf("missing final PG column %s.%s", c.Table, c.Name)
		}
		wantType := ""
		switch c.DataType {
		case "timestamp with time zone":
			wantType = "DATETIME"
		case "ARRAY", "jsonb":
			wantType = "TEXT"
		case "boolean":
			wantType = "BOOLEAN"
		}
		if wantType != "" && columnTypes[c.Table+"."+c.Name] != wantType {
			t.Errorf("lost column affinity %s.%s: want %s", c.Table, c.Name, wantType)
		}
		if c.Nullable == "NO" && nn != 1 {
			t.Errorf("lost NOT NULL: %s.%s", c.Table, c.Name)
		}
	}
	for _, name := range meta.Indexes {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=$1`, name).Scan(&n); err != nil || n != 1 {
			t.Errorf("missing final PG index %s: %v", name, err)
		}
	}
}

func TestSQLiteImmediateBusyAndContext(t *testing.T) {
	d := sqliteTestDB(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A write transaction with no statements must already hold the RESERVED lock.
	// Recover the actual writer filename without exposing application config.
	var seq int
	var schema, path string
	if err := tx.QueryRow(`PRAGMA database_list`).Scan(&seq, &schema, &path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := time.Now()
	blocked, err := other.Begin()
	if err == nil {
		blocked.Rollback()
		t.Fatal("second immediate transaction acquired the write lock")
	}
	if !IsBusyError(err) || time.Since(start) < 70*time.Millisecond {
		t.Fatal("busy timeout/classification", time.Since(start), err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err := d.ExecContext(ctx, `INSERT INTO companies(name,nkey) VALUES('blocked','blocked')`); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued writer ignored context", err)
	}
	var a, b int
	var mode string
	if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&a); err != nil || a != 1 {
		t.Fatal(a, err)
	}
	if err := tx.QueryRow(`PRAGMA busy_timeout`).Scan(&b); err != nil || b != 5000 {
		t.Fatal(b, err)
	}
	if err := tx.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatal(mode, err)
	}
}

func TestSQLiteJSONScanBoundary(t *testing.T) {
	d := sqliteTestDB(t)
	var raw json.RawMessage
	err := d.QueryRow(`SELECT '{"x":1}'`).Scan(&raw)
	if err == nil {
		t.Fatal("unexpected driver conversion; remove boundary workaround if driver changes")
	}
	var data []byte
	if err := d.QueryRow(`SELECT '{"x":1}'`).Scan(&data); err != nil || string(data) != `{"x":1}` {
		t.Fatal(string(data), err)
	}
	t.Log("SQLite TEXT scans into []byte; json.RawMessage requires an explicit DAO scanner:", err)
}

func TestSQLiteLargeIntegerExtrema(t *testing.T) {
	d := sqliteTestDB(t)
	var max, min int64
	if err := d.QueryRow(`SELECT greatest(9007199254740992,9007199254740993),least(9007199254740993,9007199254740992)`).Scan(&max, &min); err != nil {
		t.Fatal(err)
	}
	if max != 9007199254740993 || min != 9007199254740992 {
		t.Fatal("integer extrema lost precision", max, min)
	}
}

func TestSQLiteReadOnlySnapshot(t *testing.T) {
	d := sqliteTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM companies`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO companies(name,nkey) VALUES('snapshot','snapshot')`); err != nil {
		t.Fatal("read snapshot blocked writer", err)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM companies`).Scan(&count); err != nil || count != 0 {
		t.Fatal("snapshot changed", count, err)
	}
	if _, err := tx.Exec(`DELETE FROM companies`); err == nil {
		t.Fatal("snapshot permitted a write")
	}
}

func TestSQLiteBusinessTriggersAndConstraints(t *testing.T) {
	d := sqliteTestDB(t)
	var exp, task, asset, conv int64
	if err := d.QueryRow(`INSERT INTO explorations(goal) VALUES('test') RETURNING id`).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO tasks(description,goal,exploration_id) VALUES('test','test',$1) RETURNING id`, exp).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO assets(type,ip,task_ids) VALUES('ip','192.0.2.4',$1) RETURNING id`, fmt.Sprintf("[%d]", task)).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task, asset).Scan(&count)
	if count != 1 {
		t.Fatal("asset-link insert trigger", count)
	}
	d.Exec(`UPDATE assets SET task_ids='[]' WHERE id=$1`, asset)
	d.QueryRow(`SELECT COUNT(*) FROM task_asset_links`).Scan(&count)
	if count != 0 {
		t.Fatal("asset-link update trigger", count)
	}
	if err := d.QueryRow(`INSERT INTO conversations(agent_key,title) VALUES('worker','retest') RETURNING id`).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	var finding int64
	if err := d.QueryRow(`INSERT INTO findings(task_id) VALUES($1) RETURNING id`, task).Scan(&finding); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO finding_retests(finding_id,conversation_id,snapshot) VALUES($1,$2,'{}')`, finding, conv); err != nil {
		t.Fatal(err)
	}
	d.Exec(`DELETE FROM conversations WHERE id=$1`, conv)
	var state string
	var finished time.Time
	var deleted sql.NullInt64
	if err := d.QueryRow(`SELECT status,finished_at,conversation_id FROM finding_retests`).Scan(&state, &finished, &deleted); err != nil || state != "stopped" || finished.IsZero() || deleted.Valid {
		t.Fatal(state, finished, deleted, err)
	}
	if _, err := d.Exec(`INSERT INTO llm_profiles(name,format,model,retry_connect_attempts) VALUES('invalid','openai','x',-2)`); err == nil {
		t.Fatal("post-ALTER retry constraint lost")
	}
	if _, err := d.Exec(`INSERT INTO agents(key,name,role) VALUES('Invalid','invalid','worker')`); err == nil {
		t.Fatal("agent key constraint lost")
	}
	if _, err := d.Exec(`INSERT INTO assets(type,ip,task_ids) VALUES('ip','192.0.2.5','{}')`); err == nil {
		t.Fatal("non-array JSON accepted")
	}
	if _, err := d.Exec(`INSERT INTO side_question_sessions(session_key,task_id,exploration_id,run_id,version,snapshot) VALUES('session',$1,$2,1,1,'{}')`, task, exp); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprint(i)
		if _, err := d.Exec(`INSERT INTO side_question_requests(id,session_key,generation,client_id,question,status,model,snapshot_at) VALUES($1,'session',0,$1,'q','completed','{}',$2)`, id, time.Now()); err != nil {
			t.Fatal(err)
		}
		var ordinal int
		if err := d.QueryRow(`SELECT ordinal FROM side_question_requests WHERE id=$1`, id).Scan(&ordinal); err != nil || ordinal != i {
			t.Fatal("ordinal sequence", ordinal, err)
		}
		if i == 2 {
			d.Exec(`DELETE FROM side_question_requests WHERE id=$1`, id)
		}
	}
}

func TestSQLiteWriteReturningContextAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.sqlite")
	d, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.QueryContext(t.Context(), `INSERT INTO companies(name,nkey) VALUES('returning','returning') RETURNING id`)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("returning has no row")
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	// Force fresh writer connections to verify the DSN applies PRAGMAs each time.
	d.DB.SetMaxIdleConns(0)
	for i := 0; i < 2; i++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var enabled int
		if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
			t.Fatal(enabled, err)
		}
		tx.Rollback()
	}
	d.DB.SetMaxIdleConns(1)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
	if _, err := d.Exec(`DELETE FROM companies`); err == nil {
		t.Fatal("writer remained open")
	}
	if err := d.QueryRow(`SELECT 1`).Scan(&id); err == nil {
		t.Fatal("reader remained open")
	}
	if st, err := os.Stat(path + "-wal"); err == nil && st.Size() != 0 {
		t.Fatal("WAL not checkpointed", st.Size())
	}
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	d, err = OpenSQLite(moved)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.QueryRow(`SELECT id FROM companies WHERE nkey='returning'`).Scan(&id); err != nil || id != 1 {
		t.Fatal("state lost after close/move", id, err)
	}
}

func TestSQLiteRejectFutureWALWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	external, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	external.SetMaxOpenConns(1)
	external.SetMaxIdleConns(1)
	defer external.Close()
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `CREATE TABLE future_data(value TEXT)`, `INSERT INTO future_data VALUES('uncheckpointed')`, `PRAGMA user_version=999`} {
		if _, err := external.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(path string) [32]byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	mainBefore, walBefore := hash(path), hash(path+"-wal")
	if stat, err := os.Stat(path + "-wal"); err != nil || stat.Size() == 0 {
		t.Fatal("fixture requires a nonempty WAL", err)
	}
	if d, err := OpenSQLite(path); err == nil {
		d.Close()
		t.Fatal("future schema accepted")
	}
	if got := hash(path); got != mainBefore {
		t.Error("rejecting future schema changed main database bytes")
	}
	if got := hash(path + "-wal"); got != walBefore {
		t.Error("rejecting future schema changed or checkpointed WAL bytes")
	}
	var version int
	var value string
	if err := external.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 999 {
		t.Fatal(version, err)
	}
	if err := external.QueryRow(`SELECT value FROM future_data`).Scan(&value); err != nil || value != "uncheckpointed" {
		t.Fatal(value, err)
	}
}

func TestSQLiteFutureCrashFixture(t *testing.T) {
	path := os.Getenv("VH_FUTURE_CRASH_FIXTURE")
	if path == "" {
		t.Skip("subprocess fixture helper")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `CREATE TABLE future_data(value TEXT)`, `INSERT INTO future_data VALUES('committed before crash')`, `PRAGMA user_version=999`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Mimic a crashed process: no sql.DB.Close, so no last-writer checkpoint.
	os.Exit(0)
}

func TestSQLiteRejectCrashedFutureWALWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crashed-future.sqlite")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestSQLiteFutureCrashFixture$")
	child.Env = append(os.Environ(), "VH_FUTURE_CRASH_FIXTURE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v\n%s", err, output)
	}
	// No external connection remains; version=999 exists only in the WAL.
	mainBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(path + "-wal")
	if err != nil || len(walBefore) == 0 {
		t.Fatal("fixture needs nonempty WAL", err)
	}
	if d, err := OpenSQLite(path); err == nil {
		d.Close()
		t.Fatal("future schema accepted")
	}
	mainAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(mainAfter) != sha256.Sum256(mainBefore) {
		t.Error("future rejection changed main database bytes")
	}
	walAfter, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal("future rejection removed WAL", err)
	}
	if sha256.Sum256(walAfter) != sha256.Sum256(walBefore) {
		t.Error("future rejection changed WAL bytes")
	}
}
