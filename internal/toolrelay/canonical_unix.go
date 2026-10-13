//go:build !windows

package toolrelay

import "path/filepath"

func canonicalPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
