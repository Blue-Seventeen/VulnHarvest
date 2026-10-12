package server

import (
	"context"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePostgresOwnedDatabaseStartup(t *testing.T) {
	dsn := testenv.RequirePostgres(t)
	testenv.ResetContract(t)
	t.Setenv("ARTEX_PG_DSN", dsn)
	t.Setenv("VULNHARVEST_MODE", "postgres")
	probe, e := db.Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	var name string
	if e = probe.QueryRow("SELECT current_database()").Scan(&name); e != nil || name != "vh_final" {
		probe.Close()
		t.Fatalf("wrong database %q: %v", name, e)
	}
	probe.Close()
	base := t.TempDir()
	m, e := NewManagerWithOptions(ManagerOptions{Mode: "postgres", DataDir: filepath.Join(base, "data"), StateDir: filepath.Join(base, "state")})
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewWithOptions(context.Background(), m, filepath.Join(base, "skills"), m.dir, m.dir)
	if e != nil {
		m.Close()
		t.Fatal(e)
	}
	if s.m.mode != "postgres" {
		t.Fatal("wrong mode")
	}
	if _, e := os.Stat(filepath.Join(base, "state", "main.sqlite")); !os.IsNotExist(e) {
		t.Fatal("postgres created portable sqlite")
	}
	if e = s.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
}
