package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// canonicalFuturePath resolves every existing ancestor, including Windows
// junctions. Missing suffixes are appended without creating directories.
func canonicalFuturePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	probe := abs
	var suffix []string
	for {
		resolved, e := realRootPath(probe)
		if e == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(e) || filepath.Dir(probe) == probe {
			return "", e
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}
func pathsContain(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidatePortableRoots must run before opening stores or creating JWT/CA keys.
// Browse roots cannot contain state or live below it; independent external roots
// remain supported, and aliases are compared by actual canonical location.
func ValidatePortableRoots(state, data, skills string) error {
	state, err := canonicalFuturePath(state)
	if err != nil {
		return err
	}
	for name, path := range map[string]string{"data": data, "skills": skills} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("portable %s root is empty", name)
		}
		root, e := canonicalFuturePath(path)
		if e != nil {
			return e
		}
		if pathsContain(state, root) || pathsContain(root, state) {
			return fmt.Errorf("portable %s/state root overlap", name)
		}
	}
	return nil
}
