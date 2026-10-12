package toolrelay

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGenuineAssetRejectsMissingCorruptAndWrongPlatform(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "helper")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	c := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", exe, "./cmd/toolrelay")
	c.Dir = filepath.Join("..", "..")
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("build %v %s", e, b)
	}
	b, e := os.ReadFile(exe)
	if e != nil {
		t.Fatal(e)
	}
	digest := Hash(b)
	if e = VerifyAsset(exe, digest, runtime.GOOS, runtime.GOARCH); e != nil {
		t.Fatal(e)
	}
	if e = VerifyAsset(exe, digest, runtime.GOOS, "wrong"); e == nil {
		t.Fatal("wrong platform accepted")
	}
	if e = VerifyAsset(exe+"missing", digest, runtime.GOOS, runtime.GOARCH); e == nil {
		t.Fatal("missing helper accepted")
	}
	b[len(b)-1] ^= 1
	if e = os.WriteFile(exe, b, 0700); e != nil {
		t.Fatal(e)
	}
	if e = VerifyAsset(exe, digest, runtime.GOOS, runtime.GOARCH); e == nil {
		t.Fatal("corrupt helper accepted")
	}
	state, source, entries := fixture(t)
	data, _ := os.ReadFile(source)
	if _, e = Prepare(context.Background(), state, source, entries, Hash(data)); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(source, []byte("mutated helper"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = Prepare(context.Background(), state, source, entries, Hash(data)); e == nil {
		t.Fatal("rescan accepted mutated distribution asset")
	}
}
