package db

import (
	"github.com/Autumn-27/artex/internal/testenv"
	"testing"
)

func openTestDB(t *testing.T) (*DB, error) {
	t.Helper()
	var d *DB
	var err error
	if testenv.SQLite(t) {
		d, err = OpenSQLite(testenv.SQLitePath(t))
	} else {
		d, err = Open(testenv.DSN(t))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d, err
}
func contractTestDB(t *testing.T) *DB {
	t.Helper()
	testenv.ResetContract(t)
	t.Cleanup(func() { testenv.ResetContract(t) })
	d, _ := openTestDB(t)
	return d
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
