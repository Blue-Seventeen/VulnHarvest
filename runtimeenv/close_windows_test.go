package runtimeenv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tool "github.com/Autumn-27/norma/tool"
	"golang.org/x/sys/windows"
)

// Windows virus/index scanners may transiently hold a newly copied alias without
// FILE_SHARE_DELETE; Close must drain that bounded sharing conflict, not leak PATH.
func TestCloseWaitsForTransientAliasFileHandle(t *testing.T) {
	base := t.TempDir()
	m := newTestManager(t, base, "", time.Second)
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	dir := strings.Split(tool.ExecutionSnapshot().ChildPATH, string(filepath.ListSeparator))[0]
	p, _ := windows.UTF16PtrFromString(filepath.Join(dir, "curl.exe"))
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); _ = windows.CloseHandle(h); close(done) }()
	err = m.Close()
	<-done
	if err != nil {
		t.Fatalf("Close did not retry transient sharing conflict: %v", err)
	}
	if tool.ExecutionSnapshot() != nil {
		t.Fatal("Close retained published runtime")
	}
}
