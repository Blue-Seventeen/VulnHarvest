package evidence

import (
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/testenv"
	"testing"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return testenv.OpenFixture(t, false, db.OpenSQLite, db.Open)
}
func contractTestDB(t *testing.T) *db.DB {
	t.Helper()
	return testenv.OpenFixture(t, true, db.OpenSQLite, db.Open)
}
