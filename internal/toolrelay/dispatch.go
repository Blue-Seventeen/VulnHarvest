package toolrelay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var randomReader = rand.Reader

// Run handles known aliases only. A malformed or absent captured choice fails
// closed; it never starts the application or searches PATH for a fallback.
func Run() (bool, int) {
	exe, err := os.Executable()
	if err != nil {
		return false, 0
	}
	name := strings.TrimSuffix(filepath.Base(exe), ".exe")
	known := false
	for _, a := range Aliases {
		if a == name {
			known = true
		}
	}
	if !known {
		return false, 0
	}
	entry, err := Selection(exe, os.Getenv("PATH"), name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid tool relay selection:", err)
		return true, 126
	}
	if entry.Executable == "" {
		fmt.Fprintf(os.Stderr, "tool %s unavailable in this runtime selection\n", name)
		return true, 127
	}
	argv := append(append([]string(nil), entry.PrefixArgs...), os.Args[1:]...)
	return true, relayExec(entry.Executable, argv)
}
func Selection(exe, path, name string) (Entry, error) {
	helperDir := filepath.Dir(exe)
	helperHash := filepath.Base(helperDir)
	if !validHash(helperHash) || filepath.Base(filepath.Dir(helperDir)) != "cache" {
		return Entry{}, fmt.Errorf("invalid helper location")
	}
	parent := filepath.Dir(filepath.Dir(helperDir))
	root, err := CanonicalRoot(parent)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	// Captured selection is the first PATH component, always in this same root.
	parts := filepath.SplitList(path)
	if len(parts) < 2 {
		return Entry{}, fmt.Errorf("missing captured choice")
	}
	choiceDir := parts[0]
	choiceHash := filepath.Base(choiceDir)
	if !validHash(choiceHash) || !samePath(choiceDir, filepath.Join(parent, "choices", choiceHash)) || !samePath(parts[1], helperDir) {
		return Entry{}, fmt.Errorf("choice outside relay root")
	}
	for _, dir := range []string{"cache", "choices", filepath.Join("cache", helperHash), filepath.Join("choices", choiceHash)} {
		i, e := root.Lstat(dir)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return Entry{}, fmt.Errorf("redirected relay directory")
		}
	}
	b, err := readRegular(root, filepath.Join("choices", choiceHash, Manifest), MaxManifest)
	if err != nil {
		return Entry{}, err
	}
	if Hash(b) != choiceHash {
		return Entry{}, fmt.Errorf("choice content mismatch")
	}
	var choice Choice
	if json.Unmarshal(b, &choice) != nil {
		return Entry{}, fmt.Errorf("invalid manifest")
	}
	if err = validateChoice(choice); err != nil {
		return Entry{}, err
	}
	if choice.Helper != helperHash {
		return Entry{}, fmt.Errorf("helper selection mismatch")
	}
	helper, err := readRegular(root, filepath.Join("cache", helperHash, AliasName(name)), 64<<20)
	if err != nil {
		return Entry{}, err
	}
	if Hash(helper) != helperHash {
		return Entry{}, fmt.Errorf("helper content mismatch")
	}
	return choice.Entries[name], nil
}
