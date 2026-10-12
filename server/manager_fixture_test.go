package server

import (
	"context"
	"github.com/Autumn-27/artex/internal/testenv"
	"path/filepath"
	"testing"
)

// A real Manager owns stores; its Server owner stops/joins before store closure.
func newTestManager(t *testing.T, dir string) (*Manager, error) {
	t.Helper()
	mode := "postgres"
	if testenv.SQLite(t) {
		mode = "portable"
	} else {
		t.Setenv("ARTEX_PG_DSN", testenv.DSN(t))
	}
	m, err := NewManagerWithOptions(ManagerOptions{Mode: mode, DataDir: dir, StateDir: filepath.Join(t.TempDir(), "state"), SkillDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if m.owner != nil {
			if err := m.owner.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, err
}

// Register the server owner after every directory passed to the constructor,
// so testing removes those directories only after its writers have joined.
func newTestServer(t *testing.T, ctx context.Context, m *Manager, skills, data, keys string) *Server {
	t.Helper()
	s, err := NewWithOptions(ctx, m, skills, data, keys)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		// The legacy route fixture intentionally starts no background work.
		// Join every constructor publisher before installing its request lifetime.
		s.ownerWG.Wait()
		s.archiveWG.Wait()
		<-s.side.done
		s.ctx, s.cancel = context.WithCancelCause(context.Background())
	}
	t.Cleanup(func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

func cleanupTestManagerRows(t *testing.T, m *Manager, cleanup func() error) {
	t.Helper()
	t.Cleanup(func() {
		// NewManager's previously registered cleanup closes the store later.
		// This also works before a Server is created or after an early Fatal.
		if m.owner != nil {
			if err := m.owner.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
}
