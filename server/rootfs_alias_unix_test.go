//go:build !windows

package server

import (
	"path/filepath"
	"testing"
)

func rootFSAlias(t *testing.T, physical string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "ancestor-alias")
	runtimeLink(t, physical, alias)
	return alias
}
