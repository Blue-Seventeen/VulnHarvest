package toolrelay

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (string, string, map[string]Entry) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state 中文 space")
	if err := os.MkdirAll(filepath.Join(state, "runtime-path"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "relay")
	if err := os.WriteFile(source, []byte("small-test-helper"), 0700); err != nil {
		t.Fatal(err)
	}
	entries := map[string]Entry{}
	for _, a := range Aliases {
		entries[a] = Entry{}
	}
	entries["node"] = Entry{Executable: filepath.Join(base, "target 中文 space"), PrefixArgs: []string{"prefix", "中文"}}
	return state, source, entries
}

func TestRedirectedCacheAndChoiceRejected(t *testing.T) {
	state, source, entries := fixture(t)
	path, e := Prepare(context.Background(), state, source, entries)
	if e != nil {
		t.Fatal(e)
	}
	dirs := filepath.SplitList(path)
	parent := filepath.Join(state, "runtime-path")
	real := filepath.Join(parent, "choices-original")
	if e = os.Rename(filepath.Join(parent, "choices"), real); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS == "windows" {
		b, e := exec.Command("cmd", "/d", "/c", "mklink", "/J", filepath.Join(parent, "choices"), real).CombinedOutput()
		if e != nil {
			t.Fatalf("junction %s %v", b, e)
		}
	} else if e = os.Symlink(real, filepath.Join(parent, "choices")); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := os.Remove(filepath.Join(parent, "choices")); e != nil {
			t.Error(e)
		}
	})
	if _, e = Prepare(context.Background(), state, source, entries); e == nil {
		t.Fatal("redirected cache accepted")
	}
	if _, e = Selection(filepath.Join(dirs[1], AliasName("node")), path, "node"); e == nil {
		t.Fatal("redirected choice dispatched")
	}
}
func TestCacheCopyConcurrentPublicationAndCorruption(t *testing.T) {
	state, source, entries := fixture(t)
	var wg sync.WaitGroup
	results := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := Prepare(context.Background(), state, source, entries)
			results <- p
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for p := range results {
		if first == "" {
			first = p
		}
		if p != first {
			t.Fatal("nonidentical cache")
		}
	}
	dirs := filepath.SplitList(first)
	alias := filepath.Join(dirs[1], AliasName("node"))
	si, _ := os.Stat(source)
	ai, _ := os.Stat(alias)
	if os.SameFile(si, ai) {
		t.Fatal("copy test used a hardlink")
	}
	selected, e := Selection(alias, first, "node")
	if e != nil || selected.Executable != entries["node"].Executable {
		t.Fatalf("selection %v %v", selected, e)
	}
	if e = os.WriteFile(alias, []byte("corrupt"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = Prepare(context.Background(), state, source, entries); e == nil {
		t.Fatal("corrupt existing cache accepted")
	}
	if _, e = Selection(alias, first, "node"); e == nil {
		t.Fatal("corrupt relay accepted")
	}
}
func TestChoiceHashesBoundsCancellationAndPartialBuild(t *testing.T) {
	state, source, entries := fixture(t)
	first, e := Prepare(context.Background(), state, source, entries)
	if e != nil {
		t.Fatal(e)
	}
	dirs := filepath.SplitList(first)
	alias := filepath.Join(dirs[1], AliasName("node"))
	old := entries["node"]
	entries["node"] = Entry{Executable: filepath.Join(state, "new")}
	next, e := Prepare(context.Background(), state, source, entries)
	if e != nil || first == next {
		t.Fatalf("distinct selection %s %v", next, e)
	}
	selected, e := Selection(alias, first, "node")
	if e != nil || selected.Executable != old.Executable {
		t.Fatal("old selection overwritten", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Prepare(ctx, state, source, entries); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel %v", e)
	}
	entries["node"] = Entry{Executable: strings.Repeat("x", 32769)}
	if _, e = Prepare(context.Background(), state, source, entries); e == nil {
		t.Fatal("unbounded path accepted")
	}
	if e = os.WriteFile(filepath.Join(dirs[0], Manifest), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Selection(alias, first, "node"); e == nil {
		t.Fatal("manifest hash not verified")
	}
	// Partial staging artifacts are never a published selection.
	if e = os.Mkdir(filepath.Join(state, "runtime-path", "choices", ".build-partial"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = Selection(alias, filepath.Join(state, "runtime-path", "choices", ".build-partial")+string(os.PathListSeparator)+dirs[1], "node"); e == nil {
		t.Fatal("partial selection accepted")
	}
	root, e := CanonicalRoot(filepath.Join(state, "runtime-path"))
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = publish(context.Background(), root, "choices/"+strings.Repeat("a", 64), map[string][]byte{Manifest: []byte("test")}, 0600); e != nil {
		t.Fatal(e)
	}
	if e = verify(root, "choices/"+strings.Repeat("a", 64), map[string][]byte{Manifest: []byte("different")}); e == nil {
		t.Fatal("partial write not detected")
	}
}

type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestConstructionFailurePreservesPublishedChoice(t *testing.T) {
	state, source, entries := fixture(t)
	first, e := Prepare(context.Background(), state, source, entries)
	if e != nil {
		t.Fatal(e)
	}
	entries["node"] = Entry{Executable: filepath.Join(state, "different")}
	saved := randomReader
	randomReader = failingRandom{}
	defer func() { randomReader = saved }()
	if _, e = Prepare(context.Background(), state, source, entries); e == nil {
		t.Fatal("construction failure ignored")
	}
	dirs := filepath.SplitList(first)
	if _, e = Selection(filepath.Join(dirs[1], AliasName("node")), first, "node"); e != nil {
		t.Fatal("failure damaged prior snapshot", e)
	}
}

func TestSelectionPreservesCapturedPairAfterShellPATHPrefix(t *testing.T) {
	state, source, entries := fixture(t)
	path, err := Prepare(context.Background(), state, source, entries)
	if err != nil {
		t.Fatal(err)
	}
	dirs := filepath.SplitList(path)
	alias := filepath.Join(dirs[1], AliasName("node"))
	shellDir := filepath.Join(filepath.Dir(state), "shell 中文 space")
	selected, err := Selection(alias, shellDir+string(os.PathListSeparator)+path, "node")
	if err != nil || selected.Executable != entries["node"].Executable {
		t.Fatalf("shell PATH prefix changed captured selection: %+v %v", selected, err)
	}
	entries["node"] = Entry{Executable: filepath.Join(filepath.Dir(state), "another-target")}
	second, err := Prepare(context.Background(), state, source, entries)
	if err != nil {
		t.Fatal(err)
	}
	foreignState, foreignSource, foreignEntries := fixture(t)
	foreign, err := Prepare(context.Background(), foreignState, foreignSource, foreignEntries)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path string }{
		{"split-pair", strings.Join([]string{dirs[0], shellDir, dirs[1]}, string(os.PathListSeparator))},
		{"foreign-choice-root", strings.Join([]string{filepath.Join(shellDir, "choices", filepath.Base(dirs[0])), dirs[1]}, string(os.PathListSeparator))},
		{"foreign-root-only", foreign},
		{"ambiguous-pairs", path + string(os.PathListSeparator) + path},
		{"multiple-own-choices", path + string(os.PathListSeparator) + second},
		{"no-choice", shellDir + string(os.PathListSeparator) + dirs[1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Selection(alias, tc.path, "node"); err == nil {
				t.Fatal("uncaptured or ambiguous selection accepted")
			}
		})
	}
}
