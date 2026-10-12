package testenv

import "testing"

// OpenFixture owns store closure. Later row/peer/worker cleanups run first;
// contracts then reset only after the opened store and those owners are gone.
// Constructor callbacks keep this policy independent of application packages.
func OpenFixture[S interface{ Close() error }](t testing.TB, contract bool, openSQLite, openPostgres func(string) (S, error)) S {
	t.Helper()
	if contract {
		ResetContract(t)
		t.Cleanup(func() { ResetContract(t) })
	}
	var store S
	var err error
	if SQLite(t) {
		store, err = openSQLite(SQLitePath(t))
	} else {
		store, err = openPostgres(DSN(t))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
