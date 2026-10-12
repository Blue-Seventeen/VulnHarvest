package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/runtimeenv"
)

func TestRuntimeFix1FinalToolLog(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	logToolProblems(runtimeenv.Snapshot{Tools: map[string]runtimeenv.ToolStatus{
		"node":    {Name: "node", Status: "ready", AffectedFeatures: []string{"MCP"}},
		"nmap":    {Name: "nmap", Status: "missing", AffectedFeatures: []string{"network scan"}, Failures: []runtimeenv.Failure{{Source: "configured", Reason: "not found"}}},
		"browser": {Name: "browser", Status: "unverified", AffectedFeatures: []string{"browser tools"}},
		"python":  {Name: "python", Status: "ready", Failures: []runtimeenv.Failure{{Source: "configured", Reason: "invalid path token=FIX1-LOG-SECRET"}}},
	}})
	text := output.String()
	if strings.Contains(text, "node:") || strings.Contains(text, "FIX1-LOG-SECRET") {
		t.Fatalf("healthy noise or secret: %s", text)
	}
	for _, want := range []string{"nmap", "missing", "network scan", "not found", "unverified", "browser tools", "configured", "invalid path"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestRuntimeFix1AtomicMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.json")
	metadata := map[string]any{"http_address": "127.0.0.1:8787", "proxy_address": "127.0.0.1:8788", "ready": true, "padding": strings.Repeat("x", 256<<10)}
	if err := writeRuntimeMetadata(path, metadata); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var invalid []byte
	var readErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			raw, err := os.ReadFile(path)
			// Replace may briefly deny an open on Windows. A supervisor can
			// retry that sharing error; every successful read must be whole JSON.
			if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))) {
				time.Sleep(time.Millisecond)
				continue
			}
			if err != nil || !json.Valid(raw) {
				invalid, readErr = raw, err
				return
			}
		}
	}()
	var writeErr error
	for i := 0; i < 80; i++ {
		metadata["ready"] = i%2 == 0
		if writeErr = writeRuntimeMetadata(path, metadata); writeErr != nil {
			break
		}
	}
	close(stop)
	wg.Wait()
	if writeErr != nil || readErr != nil || invalid != nil {
		t.Fatalf("partial metadata read=%v bytes=%d write=%v", readErr, len(invalid), writeErr)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary metadata leaked: %v %v", entries, err)
	}
}
