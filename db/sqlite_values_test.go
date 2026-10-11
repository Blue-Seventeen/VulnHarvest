package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestPortableConnectionReuseInterfaces(t *testing.T) {
	d := portableDB(t)
	conn, err := d.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Raw(func(raw any) error {
		wrapped, ok := raw.(*sqliteValueConn)
		if !ok {
			t.Fatalf("unexpected connection %T", raw)
		}
		reset, ok := raw.(driver.SessionResetter)
		if !ok {
			t.Fatal("SQLite wrapper hides driver.SessionResetter")
		}
		valid, ok := raw.(driver.Validator)
		if !ok {
			t.Fatal("SQLite wrapper hides driver.Validator")
		}
		baseReset := wrapped.Conn.(driver.SessionResetter)
		baseValid := wrapped.Conn.(driver.Validator)
		if reset.ResetSession(context.Background()) != nil || !valid.IsValid() {
			t.Fatal("fresh connection is not usable")
		}
		// Exercise the actual underlying driver, without a fake ErrBadConn fixture.
		if err := wrapped.Conn.Close(); err != nil {
			t.Fatal(err)
		}
		if baseValid.IsValid() || valid.IsValid() {
			t.Fatal("closed connection must be invalid")
		}
		if !errors.Is(baseReset.ResetSession(context.Background()), driver.ErrBadConn) || !errors.Is(reset.ResetSession(context.Background()), driver.ErrBadConn) {
			t.Fatal("closed ResetSession lost ErrBadConn")
		}
		return driver.ErrBadConn // discard the deliberately closed raw connection
	})
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("raw discard: %v", err)
	}
	conn.Close()
	if _, err = d.Exec(`INSERT INTO server_logs(level,tag,text) VALUES('info','reuse','after closed connection')`); err != nil {
		t.Fatal(err)
	}
}

var executionProbeOnce sync.Once
var executionProbeErr error
var executionProbeChannel atomic.Pointer[chan struct{}]

func TestPortableExecutingCancellationAndReuse(t *testing.T) {
	executionProbeOnce.Do(func() {
		executionProbeErr = sqlite.RegisterScalarFunction("portable_test_execution_probe", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			if channel := executionProbeChannel.Load(); channel != nil {
				select {
				case *channel <- struct{}{}:
				default:
				}
			}
			return int64(0), nil
		})
	})
	if executionProbeErr != nil {
		t.Fatal(executionProbeErr)
	}
	d := portableDB(t)
	d.DB.SetMaxOpenConns(1)
	d.reader.SetMaxOpenConns(1)
	for _, readonly := range []bool{false, true} {
		name := "writer"
		if readonly {
			name = "reader"
		}
		t.Run(name, func(t *testing.T) {
			pool := d.DB
			if readonly {
				pool = d.reader
			}
			conn, err := pool.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var wrapped *sqliteValueConn
			if err = conn.Raw(func(raw any) error { wrapped = raw.(*sqliteValueConn); return nil }); err != nil {
				t.Fatal(err)
			}
			tx, err := conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: readonly})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			started := make(chan struct{}, 1)
			executionProbeChannel.Store(&started)
			defer executionProbeChannel.Store(nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			observed := make(chan bool, 1)
			go func() {
				select {
				case <-started:
					observed <- true
					cancel()
				case <-ctx.Done():
					observed <- false
				}
			}()
			var sum int64
			err = tx.QueryRowContext(ctx, `WITH RECURSIVE series(x) AS (
VALUES(1) UNION ALL SELECT x+1 FROM series WHERE x<1000000000
) SELECT SUM(x+portable_test_execution_probe()) FROM series`).Scan(&sum)
			if !<-observed {
				t.Fatal("cancellation never reached SQLite execution")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("running query cancellation: %v", err)
			}
			reset, ok := any(wrapped).(driver.SessionResetter)
			if !ok {
				t.Fatal("interrupted connection hides SessionResetter")
			}
			valid, ok := any(wrapped).(driver.Validator)
			if !ok {
				t.Fatal("interrupted connection hides Validator")
			}
			baseValid := wrapped.Conn.(driver.Validator).IsValid()
			if valid.IsValid() != baseValid {
				t.Fatal("interrupted IsValid result differs from underlying driver")
			}
			baseReset := wrapped.Conn.(driver.SessionResetter).ResetSession(context.Background())
			gotReset := reset.ResetSession(context.Background())
			if (baseReset == nil) != (gotReset == nil) || errors.Is(baseReset, driver.ErrBadConn) != errors.Is(gotReset, driver.ErrBadConn) {
				t.Fatalf("interrupted reset delegation: %v / %v", baseReset, gotReset)
			}
			t.Logf("after executing cancellation: underlying_valid=%t reset_bad_connection=%t", baseValid, errors.Is(baseReset, driver.ErrBadConn))
			if err = tx.Rollback(); err != nil {
				t.Fatalf("native rollback after executing cancellation: %v", err)
			}
			if err = conn.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = d.Exec(`INSERT INTO server_logs(level,tag,text) VALUES('info',$1,'after executing cancellation')`, name); err != nil {
				t.Fatalf("writer did not recover: %v", err)
			}
			var count int
			if err = d.QueryRow(`SELECT COUNT(*) FROM server_logs WHERE tag=$1`, name).Scan(&count); err != nil || count != 1 {
				t.Fatalf("reader did not recover: %d %v", count, err)
			}
		})
	}
}
