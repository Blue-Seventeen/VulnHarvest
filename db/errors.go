package db

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505"
	}
	var sq *sqlite.Error
	return errors.As(err, &sq) && (sq.Code() == 2067 || sq.Code() == 1555)
}
func IsForeignKeyViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23503"
	}
	var sq *sqlite.Error
	if !errors.As(err, &sq) {
		return false
	}
	// SQLite implements ON DELETE RESTRICT using an internal trigger, yielding
	// SQLITE_CONSTRAINT_TRIGGER (1811), not SQLITE_CONSTRAINT_FOREIGNKEY (787).
	// modernc exposes only code/message; require the exact native FK diagnostic
	// so ordinary user RAISE triggers are not mistaken for missing parents. A
	// user trigger deliberately emitting this same diagnostic is indistinguishable.
	return sq.Code() == 787 || (sq.Code() == 1811 && sq.Error() == "constraint failed: FOREIGN KEY constraint failed (1811)")
}
func IsBusyError(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "55P03"
	}
	var sq *sqlite.Error
	return errors.As(err, &sq) && (sq.Code()&255 == 5 || sq.Code()&255 == 6)
}

func IsCheckViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23514"
	}
	var sq *sqlite.Error
	return errors.As(err, &sq) && sq.Code() == 275
}
