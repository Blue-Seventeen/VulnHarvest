package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBackendSelectionIsExplicit(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"", "sqlite"}, {"sqlite", "sqlite"}, {"postgres", "postgres"}, {"auto", ""}} {
		t.Setenv("ARTEX_TEST_BACKEND", tc.value)
		got, err := Backend()
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("backend %q: %q %v", tc.value, got, err)
		}
	}
}

func TestPGGuardRejectsOtherAssets(t *testing.T) {
	for _, dsn := range []string{"", "invalid", "postgres://vh_test@127.0.0.1:5432/vh_final?sslmode=disable", "postgres://vh_test@127.0.0.1:27347/vh_runtime?sslmode=disable", "postgres://other@127.0.0.1:27347/vh_final?sslmode=disable", "postgres://vh_test@example.com:27347/vh_final?sslmode=disable"} {
		if GuardDSN(dsn) == nil {
			t.Fatal("accepted non-owned target")
		}
	}
	for _, dsn := range []string{"postgres://vh_test@127.0.0.1:27347/vh_final?sslmode=disable", "postgres://vh_test@vulnharvest-portable-test-pg:5432/vh_final?sslmode=disable"} {
		if err := GuardDSN(dsn); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResetContractRejectsLiveClientWithoutDroppingMarker(t *testing.T) {
	if os.Getenv("VH_TESTENV_GUARD_CHILD") == "1" {
		ResetContract(t)
		t.Fatal("reset unexpectedly accepted a live client")
	}
	dsn := RequirePostgres(t)
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("guard fixture open failed")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("guard fixture unavailable")
	}
	defer conn.Close()
	if err := identity(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, suiteLock); err != nil {
		t.Fatal("guard fixture lock failed")
	}
	defer func() {
		var unlocked bool
		if err := conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1)`, suiteLock).Scan(&unlocked); err != nil || !unlocked {
			t.Error("guard fixture unlock failed")
		}
	}()
	marker := fmt.Sprintf("vh_guard_marker_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, `CREATE TABLE `+marker+` (value integer NOT NULL); INSERT INTO `+marker+` VALUES(42)`); err != nil {
		t.Fatal("guard marker creation failed")
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `DROP TABLE `+marker); err != nil {
			t.Error("guard marker cleanup failed")
		}
	}()
	rows, err := conn.QueryContext(ctx, `SELECT kind, `+strings.ReplaceAll(contractMaintenancePredicate, "backend_type", "kind")+` FROM (VALUES (NULL::text),(''),('client backend'),('autovacuum worker'),('unknown worker')) AS cases(kind)`)
	if err != nil {
		t.Fatal("guard classification query failed")
	}
	for rows.Next() {
		var kind sql.NullString
		var refused bool
		if err := rows.Scan(&kind, &refused); err != nil {
			rows.Close()
			t.Fatal("guard classification scan failed")
		}
		if refused != (!kind.Valid || kind.String != "autovacuum worker") {
			rows.Close()
			t.Fatalf("guard accepted unknown/client type %q", kind.String)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal("guard classification read failed")
	}
	if err := rows.Close(); err != nil {
		t.Fatal("guard classification close failed")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, exe, "-test.run=^TestResetContractRejectsLiveClientWithoutDroppingMarker$", "-test.v")
	child.Env = append(os.Environ(), "VH_TESTENV_GUARD_CHILD=1")
	output, childErr := child.CombinedOutput()
	if childErr == nil || !strings.Contains(string(output), "refuse contract reset:") || !strings.Contains(string(output), `backend="client backend"`) {
		t.Fatalf("live-client reset refusal missing: err=%v output=%s", childErr, output)
	}
	var value int
	if err := conn.QueryRowContext(ctx, `SELECT value FROM `+marker).Scan(&value); err != nil || value != 42 {
		t.Fatalf("refused reset lost marker/table: value=%d err=%v", value, err)
	}
}
