//go:build windows

package server

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func rootFSAlias(t *testing.T, physical string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(physical)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	alias := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(alias, physical) {
		t.Skip("NTFS 8.3 aliases unavailable on fixture volume")
	}
	a, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(physical)
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("short path is not same directory: %v", err)
	}
	t.Logf("actual 8.3 ancestor: %s -> %s", alias, physical)
	return alias
}
