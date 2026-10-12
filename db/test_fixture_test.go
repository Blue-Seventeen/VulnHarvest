package db

import (
	"github.com/Autumn-27/artex/internal/testenv"
	"testing"
)

func openTestDB(t *testing.T) (*DB, error) {
	t.Helper()
	return testenv.OpenFixture(t, false, OpenSQLite, Open), nil
}
func contractTestDB(t *testing.T) *DB {
	t.Helper()
	return testenv.OpenFixture(t, true, OpenSQLite, Open)
}

func reopenTestDB(t *testing.T, d *DB) (*DB, error) {
	t.Helper()
	if !d.IsSQLite() {
		return Open(testenv.DSN(t))
	}
	var seq int
	var name, path string
	if err := d.DB.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return OpenSQLite(path)
}
