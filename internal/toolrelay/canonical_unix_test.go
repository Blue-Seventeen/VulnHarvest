//go:build !windows

package toolrelay

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func ancestorAlias(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "ancestor alias")
	directoryRedirect(t, link, target)
	return link
}

type relayReaderFunc func([]byte) (int, error)

func (f relayReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestPrepareAtRejectsRootReplacedDuringPublication(t *testing.T) {
	state, source, entries := fixture(t)
	root, err := CanonicalRoot(filepath.Join(state, "runtime-path"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	savedReader := randomReader
	t.Cleanup(func() { randomReader = savedReader })
	changed := false
	randomReader = relayReaderFunc(func(p []byte) (int, error) {
		if !changed {
			changed = true
			if err := os.Rename(root.Name(), root.Name()+"-saved"); err != nil {
				return 0, err
			}
			if err := os.Mkdir(root.Name(), 0700); err != nil {
				return 0, err
			}
		}
		return savedReader.Read(p)
	})
	path, err := PrepareAt(context.Background(), root, source, entries)
	if err == nil || path != "" {
		t.Fatalf("published PATH to replaced root: path=%q error=%v", path, err)
	}
	files, err := os.ReadDir(root.Name())
	if err != nil || len(files) != 0 {
		t.Fatalf("confined publication wrote into replacement: %v %v", files, err)
	}
	if !changed {
		t.Fatal("fixture did not replace root during publication")
	}
}
