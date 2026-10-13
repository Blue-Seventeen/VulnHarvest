// Package toolrelay is the standard-library-only portable tool relay protocol.
package toolrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const Manifest = ".tool-relay.json"
const MaxManifest = 128 << 10

var Aliases = []string{"node", "python", "python3", "npm", "npx", "rg", "nmap", "curl"}

type Entry struct {
	Executable string   `json:"executable,omitempty"`
	PrefixArgs []string `json:"prefix_args,omitempty"`
}
type Choice struct {
	Version int              `json:"version"`
	Helper  string           `json:"helper"`
	Entries map[string]Entry `json:"entries"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func AliasName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// CanonicalRoot allows ancestor aliases, but never a redirected relay root leaf.
// Its name is the canonical anchor used by both PATH publication and dispatch.
// os.Root confines actual file operations to the opened directory.
func CanonicalRoot(path string) (*os.Root, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	real, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	// Windows junctions are ModeIrregular, not necessarily ModeSymlink, and
	// EvalSymlinks may leave them unchanged. Require an ordinary directory.
	if info.Mode().Type() != os.ModeDir {
		return nil, fmt.Errorf("relay root redirected: requested=%s evaluated=%s", rootDiagnosticPath(path), rootDiagnosticPath(real))
	}
	if len(real) > 32768 || strings.ContainsRune(real, os.PathListSeparator) {
		return nil, fmt.Errorf("relay root exceeds PATH bounds")
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	current, statErr := os.Lstat(real)
	if err != nil || statErr != nil || current.Mode().Type() != os.ModeDir || !os.SameFile(info, opened) || !os.SameFile(current, opened) {
		root.Close()
		return nil, fmt.Errorf("relay root identity changed")
	}
	return root, nil
}

// checkRoot keeps a long-lived manager anchored to its original directory even
// if the canonical pathname is replaced by another ordinary directory.
func checkRoot(root *os.Root) error {
	current, err := CanonicalRoot(root.Name())
	if err != nil {
		return err
	}
	defer current.Close()
	before, err := root.Stat(".")
	after, statErr := current.Stat(".")
	if err != nil || statErr != nil || !samePath(root.Name(), current.Name()) || !os.SameFile(before, after) {
		return fmt.Errorf("relay root identity changed")
	}
	return nil
}
func rootDiagnosticPath(path string) string {
	// Bound before quoting; %q escapes controls and any partial UTF-8 suffix.
	if len(path) > 1024 {
		path = path[:1024] + "...[truncated]"
	}
	return fmt.Sprintf("%q", path)
}
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
func readRegular(root *os.Root, name string, limit int64, executable ...bool) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid relay file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("relay file identity changed")
	}
	// Verify the opened alias, not just the packaged source. Manifests remain
	// intentionally 0600; Windows build hosts expose no Unix execute bits.
	if len(executable) > 0 && executable[0] && runtime.GOOS != "windows" && opened.Mode()&0111 == 0 {
		return nil, fmt.Errorf("relay helper is not executable")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, fmt.Errorf("relay file too large")
	}
	return b, err
}
func validateChoice(c Choice) error {
	if c.Version != 1 || !validHash(c.Helper) || len(c.Entries) != len(Aliases) {
		return fmt.Errorf("invalid relay choice")
	}
	for _, name := range Aliases {
		e, ok := c.Entries[name]
		if !ok {
			return fmt.Errorf("missing relay entry")
		}
		if len(e.Executable) > 32768 || strings.ContainsRune(e.Executable, 0) || strings.ContainsRune(e.Executable, os.PathListSeparator) || len(e.PrefixArgs) > 64 {
			return fmt.Errorf("relay path or argv exceeds bounds")
		}
		if e.Executable != "" && !filepath.IsAbs(e.Executable) {
			return fmt.Errorf("relay target must be absolute")
		}
		for _, a := range e.PrefixArgs {
			if len(a) > 32768 || strings.ContainsRune(a, 0) {
				return fmt.Errorf("relay argv exceeds bounds")
			}
		}
	}
	return nil
}

// Prepare publishes only complete immutable directories. Ordinary copies are
// intentional: correctness never depends on hardlinks or an NTFS filesystem.
func Prepare(ctx context.Context, state, source string, entries map[string]Entry, expectedDigest ...string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	root, err := CanonicalRoot(filepath.Join(state, "runtime-path"))
	if err != nil {
		return "", err
	}
	defer root.Close()
	return PrepareAt(ctx, root, source, entries, expectedDigest...)
}

// PrepareAt publishes using a previously captured canonical root, without
// reopening or adopting a replacement directory for its confined operations.
func PrepareAt(ctx context.Context, root *os.Root, source string, entries map[string]Entry, expectedDigest ...string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err := checkRoot(root); err != nil {
		return "", err
	}
	f, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("relay executable unavailable: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		f.Close()
		return "", fmt.Errorf("invalid relay executable")
	}
	helper, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(helper) == 0 || len(helper) > 64<<20 {
		return "", fmt.Errorf("invalid relay executable size")
	}
	helperHash := Hash(helper)
	if len(expectedDigest) > 0 && expectedDigest[0] != "" && helperHash != expectedDigest[0] {
		return "", fmt.Errorf("relay asset content mismatch")
	}
	c := Choice{Version: 1, Helper: helperHash, Entries: entries}
	if err = validateChoice(c); err != nil {
		return "", err
	}
	manifest, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if len(manifest) > MaxManifest {
		return "", fmt.Errorf("relay manifest exceeds bounds")
	}
	parent := root.Name()
	for _, d := range []string{"cache", "choices"} {
		if err = root.Mkdir(d, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
		i, e := root.Lstat(d)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("relay cache redirected")
		}
	}
	helperDir := filepath.Join("cache", helperHash)
	files := map[string][]byte{}
	for _, name := range Aliases {
		files[AliasName(name)] = helper
	}
	if err = publish(ctx, root, helperDir, files, 0700); err != nil {
		return "", fmt.Errorf("relay helper cache: %w", err)
	}
	choiceDir := filepath.Join("choices", Hash(manifest))
	if err = publish(ctx, root, choiceDir, map[string][]byte{Manifest: manifest}, 0600); err != nil {
		return "", fmt.Errorf("relay choice cache: %w", err)
	}
	if err = checkRoot(root); err != nil {
		return "", err
	}
	return filepath.Join(parent, choiceDir) + string(os.PathListSeparator) + filepath.Join(parent, helperDir), nil
}
func verify(root *os.Root, dir string, files map[string][]byte) error {
	info, err := root.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache directory redirected")
	}
	sub, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer sub.Close()
	listing, err := sub.Open(".")
	if err != nil {
		return err
	}
	names, err := listing.Readdirnames(-1)
	listing.Close()
	if err != nil {
		return err
	}
	if len(names) != len(files) {
		return fmt.Errorf("unexpected cache files")
	}
	for name, want := range files {
		got, err := readRegular(sub, name, int64(len(want)), name != Manifest)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("corrupt cache file %s", name)
		}
	}
	return nil
}
func publish(ctx context.Context, root *os.Root, dir string, files map[string][]byte, mode os.FileMode) (resultErr error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, err := root.Lstat(dir); err == nil {
		return verify(root, dir, files)
	} else if !os.IsNotExist(err) {
		return err
	}
	nonce := make([]byte, 16)
	// CreateTemp is not available on os.Root; random names plus exclusive mkdir
	// reserve the staging directory without following a pre-existing artifact.
	if _, err := io.ReadFull(randomReader, nonce); err != nil {
		return err
	}
	stage := filepath.Join(filepath.Dir(dir), ".build-"+hex.EncodeToString(nonce))
	if err := root.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.RemoveAll(stage)) }()
	sub, err := root.OpenRoot(stage)
	if err != nil {
		return err
	}
	for name, b := range files {
		if ctx.Err() != nil {
			sub.Close()
			return ctx.Err()
		}
		f, e := sub.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if e != nil {
			sub.Close()
			return e
		}
		n, e := f.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			sub.Close()
			return e
		}
	}
	if err = sub.Close(); err != nil {
		return err
	}
	if err = verify(root, stage, files); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = root.Rename(stage, dir)
		if err == nil {
			break
		}
		if e := verify(root, dir, files); e == nil {
			return nil
		}
		if runtime.GOOS != "windows" || (!errors.Is(err, syscall.Errno(5)) && !errors.Is(err, syscall.Errno(32))) || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err != nil {
		if e := verify(root, dir, files); e != nil {
			return err
		}
	}
	return verify(root, dir, files)
}
