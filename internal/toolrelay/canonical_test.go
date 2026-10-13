package toolrelay

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Directory redirects deliberately use a real junction on Windows: EvalSymlinks
// alone does not identify these as redirects with current Go/Windows semantics.
func directoryRedirect(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		b, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			t.Fatalf("junction: %s %v", b, err)
		}
	} else if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
}

func TestCanonicalRootRejectsDirectoryRedirect(t *testing.T) {
	base := t.TempDir()
	target, link := filepath.Join(base, "real"), filepath.Join(base, "runtime-path")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	directoryRedirect(t, link, target)
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("redirect mode=%s requested=%q evaluated=%q", info.Mode(), link, evaluated)
	root, err := CanonicalRoot(link)
	if root != nil {
		root.Close()
		t.Fatal("directory redirect returned a root handle")
	}
	if err == nil {
		t.Fatal("directory redirect accepted")
	}
}

func TestPrepareAndSelectionAcceptAncestorAlias(t *testing.T) {
	state, source, entries := fixture(t)
	aliasBase := ancestorAlias(t, filepath.Dir(state))
	checkAncestorSelection(t, state, source, aliasBase, entries)
}

func checkAncestorSelection(t *testing.T, state, source, aliasBase string, entries map[string]Entry) {
	t.Helper()
	aliasState := filepath.Join(aliasBase, filepath.Base(state))
	path, err := Prepare(context.Background(), aliasState, source, entries)
	if err != nil {
		t.Fatal(err)
	}
	dirs := filepath.SplitList(path)
	canonicalState, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		if filepath.Dir(filepath.Dir(dir)) != filepath.Join(canonicalState, "runtime-path") {
			t.Fatalf("Prepare published alias rather than canonical anchor: %q", path)
		}
	}
	// os.Executable can expand the Windows short name independently of PATH.
	for _, base := range []string{canonicalState, aliasState} {
		exe := filepath.Join(base, "runtime-path", "cache", filepath.Base(dirs[1]), AliasName("node"))
		selected, err := Selection(exe, path, "node")
		if err != nil || selected.Executable != entries["node"].Executable {
			t.Fatalf("canonical selection for %q: %+v %v", exe, selected, err)
		}
	}
}

func TestCapturedSelectionRejectsReplacedRoot(t *testing.T) {
	state, source, entries := fixture(t)
	path, err := Prepare(context.Background(), state, source, entries)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Dir(filepath.Dir(filepath.SplitList(path)[1]))
	saved := rootPath + "-saved"
	if err := os.Rename(rootPath, saved); err != nil {
		t.Fatal(err)
	}
	directoryRedirect(t, rootPath, saved)
	if _, err := Prepare(context.Background(), state, source, entries); err == nil {
		t.Fatal("Prepare accepted a replaced root")
	}
	exe := filepath.Join(filepath.SplitList(path)[1], AliasName("node"))
	if _, err := Selection(exe, path, "node"); err == nil {
		t.Fatal("captured selection followed replaced root")
	}
}

func TestRedirectedRelayDescendantsRejected(t *testing.T) {
	for _, descendant := range []string{"cache", "choices", "helper-hash", "choice-hash"} {
		t.Run(descendant, func(t *testing.T) {
			state, source, entries := fixture(t)
			path, err := Prepare(context.Background(), state, source, entries)
			if err != nil {
				t.Fatal(err)
			}
			dirs := filepath.SplitList(path)
			redirect := map[string]string{"cache": filepath.Dir(dirs[1]), "choices": filepath.Dir(dirs[0]), "helper-hash": dirs[1], "choice-hash": dirs[0]}[descendant]
			saved := redirect + "-saved"
			if err := os.Rename(redirect, saved); err != nil {
				t.Fatal(err)
			}
			directoryRedirect(t, redirect, saved)
			if _, err := Prepare(context.Background(), state, source, entries); err == nil {
				t.Fatal("Prepare accepted redirected descendant")
			}
			if _, err := Selection(filepath.Join(dirs[1], AliasName("node")), path, "node"); err == nil {
				t.Fatal("Selection accepted redirected descendant")
			}
		})
	}
}
