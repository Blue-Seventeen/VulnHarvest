//go:build !windows

package server

import "path/filepath"

func realRootPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
