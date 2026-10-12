package runtimeenv

import (
	"net"
	"path/filepath"
	"testing"
)

func TestInstanceLockRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first, err := LockInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LockInstance(path)
	if err == nil {
		second.Close()
		t.Fatal("second instance acquired held lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err = LockInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}
func TestPortableListenerRetainedFallback(t *testing.T) {
	owner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	got, err := Listen("portable", owner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if got.Addr().String() == owner.Addr().String() {
		t.Fatal("stole occupied listener")
	}
	if !got.Addr().(*net.TCPAddr).IP.IsLoopback() {
		t.Fatal("nonloopback fallback")
	}
	if fixed, err := Listen("postgres", owner.Addr().String()); err == nil {
		fixed.Close()
		t.Fatal("postgres unexpectedly changed fixed port")
	}
	if bad, err := Listen("portable", "0.0.0.0:0"); err == nil {
		bad.Close()
		t.Fatal("portable public bind")
	}
}
