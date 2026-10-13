package runtimeenv

import (
	"strings"
	"syscall"
	"testing"
)

func runtimeAncestorAlias(t *testing.T, target string) string {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := syscall.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		t.Fatalf("GetShortPathName: length=%d error=%v", n, err)
	}
	short := syscall.UTF16ToString(buf[:n])
	if strings.EqualFold(short, target) {
		t.Skip("fixture volume has no available 8.3 alias; no machinewide setting changed")
	}
	t.Logf("actual 8.3 alias: %q -> %q", short, target)
	return short
}
