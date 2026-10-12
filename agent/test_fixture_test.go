package agent

import (
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/testenv"
	"testing"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	var d *db.DB
	var err error
	if testenv.SQLite(t) {
		d, err = db.OpenSQLite(testenv.SQLitePath(t))
	} else {
		d, err = db.Open(testenv.DSN(t))
	}
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
func contractTestDB(t *testing.T) *db.DB {
	t.Helper()
	testenv.ResetContract(t)
	t.Cleanup(func() { testenv.ResetContract(t) })
	return openTestDB(t)
}
