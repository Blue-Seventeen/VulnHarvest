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

// CanonicalRoot rejects redirected roots and descendants. os.Root confines all
// actual file operations even if a directory is replaced after this check.
func CanonicalRoot(path string) (*os.Root, error) {
	path = filepath.Clean(path)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if !samePath(real, path) {
		return nil, fmt.Errorf("relay root redirected")
	}
	return os.OpenRoot(path)
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
	parent := filepath.Join(state, "runtime-path")
	root, err := CanonicalRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
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
