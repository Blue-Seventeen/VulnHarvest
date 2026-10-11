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
	return errors.As(err, &sq) && sq.Code() == 787
}
func IsBusyError(err error) bool {
	var sq *sqlite.Error
	return errors.As(err, &sq) && (sq.Code()&255 == 5 || sq.Code()&255 == 6)
}
