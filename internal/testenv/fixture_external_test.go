package testenv_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/testenv"
)

type failingCloseStore struct{ *db.DB }

func (s *failingCloseStore) Close() error {
	if err := s.DB.Close(); err != nil {
		return err
	}
	return errors.New("fixture close failure propagated")
}

func TestSharedFixtureReportsConstructorAndCloseErrors(t *testing.T) {
	if mode := os.Getenv("VH_SHARED_FIXTURE_CHILD"); mode != "" {
		switch mode {
		case "constructor":
			testenv.OpenFixture(t, false, func(path string) (*db.DB, error) {
				// Use a real non-directory parent so OpenSQLite cannot initialize.
				if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				d, err := db.OpenSQLite(filepath.Join(path, "state.sqlite"))
				if err != nil {
					return d, fmt.Errorf("fixture constructor failure propagated: %w", err)
				}
				return d, nil
			}, db.Open)
			t.Fatal("fixture unexpectedly continued after constructor failure")
		case "close":
			var d *db.DB
			t.Cleanup(func() {
				if d == nil || d.Ping() == nil {
					t.Error("close failure did not close the real store")
					return
				}
				if err := os.WriteFile(os.Getenv("VH_SHARED_FIXTURE_MARKER"), []byte("store closed"), 0600); err != nil {
					t.Error(err)
				}
			})
			testenv.OpenFixture(t, false, func(path string) (*failingCloseStore, error) {
				var err error
				d, err = db.OpenSQLite(path)
				return &failingCloseStore{d}, err
			}, func(dsn string) (*failingCloseStore, error) {
				return nil, errors.New("unexpected PostgreSQL constructor")
			})
		default:
			t.Fatal("unknown fixture child mode")
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"constructor", "close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			marker := filepath.Join(t.TempDir(), "closed.txt")
			child := exec.CommandContext(ctx, exe, "-test.run=^TestSharedFixtureReportsConstructorAndCloseErrors$", "-test.v")
			child.Env = append(os.Environ(), "ARTEX_TEST_BACKEND=sqlite", "ARTEX_PG_DSN=", "VH_SHARED_FIXTURE_CHILD="+mode, "VH_SHARED_FIXTURE_MARKER="+marker)
			out, childErr := child.CombinedOutput()
			want := "fixture " + mode + " failure propagated"
			if childErr == nil || ctx.Err() != nil || !strings.Contains(string(out), want) || strings.Contains(string(out), "unexpectedly continued") {
				t.Fatalf("fixture %s failure invisible: err=%v output=%s", mode, childErr, out)
			}
			if mode == "close" {
				got, err := os.ReadFile(marker)
				if err != nil || string(got) != "store closed" {
					t.Fatalf("real store close marker=%q err=%v output=%s", got, err, out)
				}
			}
		})
	}
}
