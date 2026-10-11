package runtimeenv

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// An OS Start failure happens after the private job and output pipes have been
// created. Missing-executable probes must release those handles and join their
// output copier without ever starting a process or exposing raw start errors.
func TestProbeOwnerWindowsStartFailureReleasesJobAndPipes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.exe")
	run := func() {
		t.Helper()
		cmd := exec.Command(missing, "--version")
		var out bytes.Buffer
		runErr, cleanupErr := runOwnedProbe(context.Background(), cmd, &out)
		if runErr == nil || cleanupErr != nil || cmd.Process != nil || out.Len() != 0 {
			t.Fatalf("failed Start ownership: run=%v cleanup=%v started=%t output=%q", runErr, cleanupErr, cmd.Process != nil, out.String())
		}
	}
	countProc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	handleCount := func() uint32 {
		t.Helper()
		var count uint32
		ok, _, err := countProc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			t.Fatal(err)
		}
		return count
	}
	run() // warm lazy OS/runtime handle initialization before comparing counts
	before := handleCount()
	for i := 0; i < 16; i++ {
		run()
	}
	after := handleCount()
	if after > before+2 {
		t.Fatalf("failed probes leaked OS handles: before=%d after=%d", before, after)
	}
}
