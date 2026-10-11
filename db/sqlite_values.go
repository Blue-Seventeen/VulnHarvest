package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"modernc.org/sqlite"
)

const sqliteTimeLayout = "2006-01-02 15:04:05.000000000"

// Only bound values are normalized. SQL text is never altered, and database/sql
// applies the same checker to pool calls, prepared statements and native *sql.Tx.
type sqliteValueDriver struct{ base driver.Driver }
type sqliteValueConn struct{ driver.Conn }

func init() {
	pool, err := sql.Open("sqlite", "")
	if err != nil {
		panic(err)
	}
	base := pool.Driver()
	pool.Close()
	sql.Register("artex-sqlite", sqliteValueDriver{base: base})
}
func (d sqliteValueDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &sqliteValueConn{c}, nil
}
func (c *sqliteValueConn) CheckNamedValue(v *driver.NamedValue) error {
	value, err := driver.DefaultParameterConverter.ConvertValue(v.Value)
	if err != nil {
		return err
	}
	if t, ok := value.(time.Time); ok {
		value = t.UTC().Format(sqliteTimeLayout)
	}
	v.Value = value
	return nil
}
func (c *sqliteValueConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *sqliteValueConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *sqliteValueConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *sqliteValueConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *sqliteValueConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }

// JSONScanner handles SQLite TEXT, PostgreSQL JSONB []byte and SQL NULL without
// sharing driver-specific assumptions across DAO scan destinations.
type JSONScanner struct{ Target *json.RawMessage }

// DAO bind sites opt in explicitly. SQLite JSON columns store TEXT rather than
// depending on the JSON extension's compatibility handling for JSON BLOBs.
func (d *DB) jsonValue(raw []byte) any {
	if d.IsSQLite() {
		return string(raw)
	}
	return raw
}

func (s JSONScanner) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*s.Target = nil
	case string:
		*s.Target = append((*s.Target)[:0], v...)
	case []byte:
		*s.Target = append((*s.Target)[:0], v...)
	default:
		return fmt.Errorf("scan JSON: unsupported %T", value)
	}
	return nil
}

// Aggregate expressions such as MAX(DATETIME) lose SQLite's declared-column
// type metadata. Decode only those destinations explicitly; ordinary DATETIME
// columns continue to use the driver's time.Time values.
type nullableTimeScanner struct{ Target *sql.NullTime }

func (s nullableTimeScanner) Scan(value any) error {
	if value == nil {
		*s.Target = sql.NullTime{}
		return nil
	}
	if t, ok := value.(time.Time); ok {
		*s.Target = sql.NullTime{Time: t, Valid: true}
		return nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("scan datetime: unsupported %T", value)
	}
	for _, layout := range []string{sqliteTimeLayout, "2006-01-02 15:04:05.999999999", time.RFC3339Nano} {
		if t, err := time.Parse(layout, text); err == nil {
			*s.Target = sql.NullTime{Time: t, Valid: true}
			return nil
		}
	}
	return fmt.Errorf("scan datetime: invalid value %q", text)
}

// Runtime table creation belongs to versioned SQLite migrations. These checks
// fail clearly for an incomplete store instead of executing PostgreSQL DDL.
func (d *DB) verifySQLiteTable(table string, columns ...string) error {
	// All identifiers are code-owned constants from the two callers.
	var version int
	if err := d.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != sqliteSchemaVersion {
		return fmt.Errorf("SQLite schema version %d; expected %d", version, sqliteSchemaVersion)
	}
	rows, err := d.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var ordinal, required, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&ordinal, &name, &typ, &required, &def, &pk); err != nil {
			return err
		}
		seen[name] = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(seen) == 0 {
		return fmt.Errorf("SQLite versioned table %s missing", table)
	}
	for _, name := range columns {
		if !seen[name] {
			return fmt.Errorf("SQLite versioned column %s.%s missing", table, name)
		}
	}
	return nil
}

func sqliteJSONOverlay(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var merged map[string]json.RawMessage
	for _, arg := range args {
		var raw []byte
		switch v := arg.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			return nil, fmt.Errorf("JSON overlay input %T", arg)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		if obj == nil {
			return nil, fmt.Errorf("JSON overlay requires object")
		}
		if merged == nil {
			merged = make(map[string]json.RawMessage)
		}
		for key, value := range obj {
			merged[key] = value
		}
	}
	raw, err := json.Marshal(merged)
	return string(raw), err
}

func sqliteTimeShift(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	text, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("time shift input %T", args[0])
	}
	parsed, err := time.Parse("2006-01-02 15:04:05.999999999", text)
	if err != nil {
		return nil, err
	}
	var seconds float64
	switch v := args[1].(type) {
	case float64:
		seconds = v
	case int64:
		seconds = float64(v)
	default:
		return nil, fmt.Errorf("time shift seconds %T", args[1])
	}
	return parsed.Add(time.Duration(seconds * float64(time.Second))).UTC().Format(sqliteTimeLayout), nil
}
