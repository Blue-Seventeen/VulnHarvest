package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

//go:embed sqlite_schema.sql
var sqliteSchemaSQL string

const sqliteSchemaVersion = 1
const sqliteBusyTimeout = 5000

var sqliteFunctionsOnce sync.Once
var sqliteFunctionsErr error

// OpenSQLite opens an on-disk portable store. It never reads PostgreSQL config
// and intentionally leaves builtin seeding to the startup initialization phase.
func OpenSQLite(path string) (_ *DB, err error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, fmt.Errorf("SQLite requires an on-disk path")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err = registerSQLiteFunctions(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	// Absolute Windows paths need the leading slash in a file URI.
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(ON)")
	params.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout))
	params.Add("_pragma", "recursive_triggers(OFF)")
	params.Set("_txlock", "immediate")
	u.RawQuery = params.Encode()
	writer, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	d := &DB{DB: writer, sqlite: true}
	defer func() {
		if err != nil {
			// Failure cleanup must not checkpoint a database whose version we
			// rejected. Close only the pools; normal Close owns the checkpoint.
			if d.reader != nil {
				d.reader.Close()
			}
			writer.Close()
		}
	}()
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	if err = writer.Ping(); err != nil {
		return nil, fmt.Errorf("open SQLite writer: %w", err)
	}
	// Inspect before changing journal mode so a future-version database is untouched.
	var version int
	if err = writer.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version > sqliteSchemaVersion {
		return nil, fmt.Errorf("SQLite schema %d is newer than supported %d", version, sqliteSchemaVersion)
	}
	if _, err = writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if err = migrateSQLite(writer, path, []string{sqliteSchemaSQL}); err != nil {
		return nil, err
	}
	params.Set("_txlock", "deferred")
	params.Add("_pragma", "query_only(ON)")
	u.RawQuery = params.Encode()
	d.reader, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	d.reader.SetMaxOpenConns(8)
	d.reader.SetMaxIdleConns(8)
	if err = d.reader.Ping(); err != nil {
		return nil, fmt.Errorf("open SQLite reader: %w", err)
	}
	return d, nil
}

// migrateSQLite keeps each schema transition atomic. Existing databases receive
// a consistent standalone VACUUM INTO backup before any versioned DDL is run.
func migrateSQLite(pool *sql.DB, path string, migrations []string) error {
	var version int
	if err := pool.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("SQLite schema %d is newer than supported %d", version, len(migrations))
	}
	if version == len(migrations) {
		return nil
	}
	var existing int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		backup := fmt.Sprintf("%s.backup-v%d-%d", path, version, time.Now().UnixNano())
		if _, err := pool.Exec(`VACUUM INTO $1`, backup); err != nil {
			return fmt.Errorf("backup SQLite before migration: %w", err)
		}
		if err := os.Chmod(backup, 0600); err != nil {
			return fmt.Errorf("protect SQLite backup: %w", err)
		}
	}
	tx, err := pool.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("SQLite migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migration: %w", err)
	}
	return nil
}

func (d *DB) IsSQLite() bool { return d != nil && d.sqlite }
func (d *DB) SQL(postgres, sqlite string) string {
	if d.IsSQLite() {
		return sqlite
	}
	return postgres
}

// SELECTs run on the read pool. Everything else (including WITH statements,
// write RETURNING queries and mutating PRAGMAs) conservatively uses the writer.
// No query text is changed. Ambiguous read statements remain safe on the writer.
func (d *DB) queryPool(query string) *sql.DB {
	if !d.IsSQLite() || d.reader == nil {
		return d.DB
	}
	q := strings.TrimSpace(query)
	// Strip leading SQL comments for ordinary shared queries.
	for {
		if strings.HasPrefix(q, "--") {
			if i := strings.IndexByte(q, '\n'); i >= 0 {
				q = strings.TrimSpace(q[i+1:])
				continue
			}
			break
		}
		if strings.HasPrefix(q, "/*") {
			if i := strings.Index(q, "*/"); i >= 0 {
				q = strings.TrimSpace(q[i+2:])
				continue
			}
			break
		}
		break
	}
	upper := strings.ToUpper(q)
	if strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "SELECT\t") {
		return d.reader
	}
	if strings.HasPrefix(upper, "PRAGMA ") && !strings.ContainsAny(q, "=;") {
		p := strings.TrimSpace(upper[len("PRAGMA "):])
		if p == "FOREIGN_KEYS" || p == "BUSY_TIMEOUT" || p == "JOURNAL_MODE" || p == "QUERY_ONLY" || p == "USER_VERSION" || strings.HasPrefix(p, "TABLE_INFO(") || strings.HasPrefix(p, "FOREIGN_KEY_LIST(") || strings.HasPrefix(p, "INDEX_LIST(") {
			return d.reader
		}
	}
	return d.DB
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) { return d.DB.Exec(query, args...) }
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, query, args...)
}
func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.queryPool(query).Query(query, args...)
}
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.queryPool(query).QueryContext(ctx, query, args...)
}
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.queryPool(query).QueryRow(query, args...)
}
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.queryPool(query).QueryRowContext(ctx, query, args...)
}
func (d *DB) Begin() (*sql.Tx, error) { return d.DB.Begin() }
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if d.IsSQLite() && opts != nil && opts.ReadOnly {
		return d.reader.BeginTx(ctx, opts)
	}
	return d.DB.BeginTx(ctx, opts)
}

func (d *DB) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	var errs []error
	if d.reader != nil {
		errs = append(errs, d.reader.Close())
	}
	if d.IsSQLite() {
		var busy, log, checkpointed int
		if err := d.DB.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
			if !errors.Is(err, sql.ErrConnDone) && !strings.Contains(err.Error(), "database is closed") {
				errs = append(errs, err)
			}
		} else if busy != 0 {
			errs = append(errs, fmt.Errorf("SQLite checkpoint busy: %d WAL frames remain", log-checkpointed))
		}
	}
	errs = append(errs, d.DB.Close())
	return errors.Join(errs...)
}

func registerSQLiteFunctions() error {
	sqliteFunctionsOnce.Do(func() {
		funcs := []struct {
			name string
			n    int32
			fn   func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error)
		}{
			{"now", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
				return time.Now().UTC().Format("2006-01-02 15:04:05.000"), nil
			}},
			{"concat_ws", -1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if len(args) == 0 {
					return nil, fmt.Errorf("concat_ws needs a separator")
				}
				if args[0] == nil {
					return nil, nil
				}
				parts := []string{}
				for _, v := range args[1:] {
					if v != nil {
						parts = append(parts, fmt.Sprint(v))
					}
				}
				return strings.Join(parts, fmt.Sprint(args[0])), nil
			}},
			{"greatest", -1, sqliteExtremum(true)},
			{"least", -1, sqliteExtremum(false)},
		}
		for _, f := range funcs {
			if err := sqlite.RegisterScalarFunction(f.name, f.n, f.fn); err != nil {
				sqliteFunctionsErr = err
				return
			}
		}
	})
	return sqliteFunctionsErr
}

func sqliteExtremum(greatest bool) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var best driver.Value
		for _, v := range args {
			if v == nil {
				continue
			}
			if best == nil {
				best = v
				continue
			}
			comparison := 0
			switch x := v.(type) {
			case int64:
				if b, ok := best.(int64); ok {
					if x < b {
						comparison = -1
					} else if x > b {
						comparison = 1
					}
					break
				}
				var y float64
				switch b := best.(type) {
				case int64:
					y = float64(b)
				case float64:
					y = b
				default:
					return nil, fmt.Errorf("mixed scalar types")
				}
				if float64(x) < y {
					comparison = -1
				} else if float64(x) > y {
					comparison = 1
				}
			case float64:
				var y float64
				switch b := best.(type) {
				case int64:
					y = float64(b)
				case float64:
					y = b
				default:
					return nil, fmt.Errorf("mixed scalar types")
				}
				if x < y {
					comparison = -1
				} else if x > y {
					comparison = 1
				}
			case string:
				b, ok := best.(string)
				if !ok {
					return nil, fmt.Errorf("mixed scalar types")
				}
				comparison = strings.Compare(x, b)
			default:
				return nil, fmt.Errorf("unsupported scalar type %T", v)
			}
			if (greatest && comparison > 0) || (!greatest && comparison < 0) {
				best = v
			}
		}
		return best, nil
	}
}
