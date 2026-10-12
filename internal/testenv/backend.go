// Package testenv is the explicit, disposable business-test backend policy.
// It deliberately does not import application storage or configuration.
package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const suiteLock int64 = 7337741002
const contractMaintenancePredicate = "backend_type IS DISTINCT FROM 'autovacuum worker'"
const contractOtherSessionPredicate = "datname=current_database() AND pid<>pg_backend_pid() AND pid<>$1 AND " + contractMaintenancePredicate

var suitePID int

func Backend() (string, error) {
	b := strings.TrimSpace(os.Getenv("ARTEX_TEST_BACKEND"))
	if b == "" {
		b = "sqlite"
	}
	if b != "sqlite" && b != "postgres" {
		return "", fmt.Errorf("ARTEX_TEST_BACKEND must be sqlite or postgres")
	}
	return b, nil
}

func SQLite(t testing.TB) bool {
	t.Helper()
	b, err := Backend()
	if err != nil {
		t.Fatal(err)
	}
	return b == "sqlite"
}

func Engines(t testing.TB) []string {
	t.Helper()
	if SQLite(t) {
		return []string{"SQLite"}
	}
	return []string{"PostgreSQL"}
}

func SQLitePath(t testing.TB) string { t.Helper(); return filepath.Join(t.TempDir(), "state.sqlite") }

func GuardDSN(dsn string) error {
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("explicit ARTEX_PG_DSN is required for postgres tests")
	}
	c, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid explicit test PostgreSQL configuration")
	}
	// Container-native runs use the exact owned network DNS and internal port.
	endpoint := c.Host == "127.0.0.1" && c.Port == 27347 || c.Host == "vulnharvest-portable-test-pg" && c.Port == 5432
	if !endpoint || c.Database != "vh_final" || c.User != "vh_test" || len(c.Fallbacks) != 0 {
		return fmt.Errorf("test PostgreSQL must target the owned vh_final/vh_test endpoint without fallbacks")
	}
	return nil
}

func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if err := GuardDSN(dsn); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func RequirePostgres(t testing.TB) string {
	t.Helper()
	if SQLite(t) {
		t.Skip("PostgreSQL-only lock/migration/runtime observation; SQLite business peer contracts run separately")
	}
	return DSN(t)
}

func identity(ctx context.Context, conn *sql.Conn) error {
	var database, user string
	if err := conn.QueryRowContext(ctx, `SELECT current_database(),current_user`).Scan(&database, &user); err != nil {
		return fmt.Errorf("test PostgreSQL identity query failed")
	}
	if database != "vh_final" || user != "vh_test" {
		return fmt.Errorf("refuse non-owned test database identity")
	}
	return nil
}

// ResetContract may only run between fully closed/joined fixtures. The suite
// lock stays pinned on another known session. Only the observed PostgreSQL
// autovacuum worker is internal maintenance; client/unknown types must be gone.
func ResetContract(t testing.TB) {
	t.Helper()
	if SQLite(t) {
		return
	}
	pool, err := sql.Open("pgx", DSN(t))
	if err != nil {
		t.Fatal("contract reset connection failed")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("contract reset unavailable")
	}
	defer conn.Close()
	if err = identity(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var other int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE `+contractOtherSessionPredicate, suitePID).Scan(&other); err != nil {
		t.Fatal("contract reset session check failed")
	}
	if other != 0 {
		rows, err := conn.QueryContext(ctx, `SELECT pid,application_name,state,backend_start,client_addr::text,client_port,backend_type,(regexp_match(query,'^[[:space:]]*([[:alpha:]]+)'))[1] FROM pg_stat_activity WHERE `+contractOtherSessionPredicate+` ORDER BY pid`, suitePID)
		if err != nil {
			t.Fatal("contract reset session diagnostics failed")
		}
		var sessions []string
		for rows.Next() {
			var pid int
			var app, state, addr, backend, verb sql.NullString
			var started sql.NullTime
			var port sql.NullInt64
			if err := rows.Scan(&pid, &app, &state, &started, &addr, &port, &backend, &verb); err != nil {
				rows.Close()
				t.Fatal("contract reset session diagnostic scan failed")
			}
			sessions = append(sessions, fmt.Sprintf("pid=%d app=%q state=%s started=%s addr=%s port=%d backend=%q verb=%q", pid, app.String, state.String, started.Time.UTC().Format(time.RFC3339Nano), addr.String, port.Int64, backend.String, verb.String))
		}
		rowErr, closeErr := rows.Err(), rows.Close()
		if rowErr != nil || closeErr != nil {
			t.Fatal("contract reset session diagnostic read failed")
		}
		t.Fatalf("refuse contract reset: %d prior fixture or external database sessions remain; suite_pid=%d sessions=%v", other, suitePID, sessions)
	}
	if _, err = conn.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public AUTHORIZATION vh_test`); err != nil {
		t.Fatal("owned contract reset failed")
	}
}

// RunSuite returns before os.Exit so unlock and connection cleanup execute.
func RunSuite(m *testing.M) (code int) {
	b, err := Backend()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if b == "sqlite" {
		return m.Run()
	}
	dsn := os.Getenv("ARTEX_PG_DSN")
	if err = GuardDSN(dsn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test PostgreSQL open failed")
		return 1
	}
	defer func() {
		if pool.Close() != nil {
			code = 1
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test PostgreSQL unavailable")
		return 1
	}
	defer func() {
		if conn.Close() != nil {
			code = 1
		}
	}()
	if err = identity(ctx, conn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&suitePID); err != nil {
		return 1
	}
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, suiteLock); err != nil {
		fmt.Fprintln(os.Stderr, "test suite lock failed")
		return 1
	}
	defer func() {
		unlockCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		var unlocked bool
		if err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, suiteLock).Scan(&unlocked); err != nil || !unlocked {
			fmt.Fprintln(os.Stderr, "test suite unlock failed")
			code = 1
		}
		suitePID = 0
	}()
	return m.Run()
}
