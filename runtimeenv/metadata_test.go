package runtimeenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalPlaywrightMetadataDoesNotExecutePackage(t *testing.T) {
	base := t.TempDir()
	packageDir := filepath.Join(base, "runtime", "node_modules", "@playwright", "mcp")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"name":"@playwright/mcp","version":"0.0.45","scripts":{"postinstall":"must-not-run"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, base, "", time.Second)
	snap, err := m.Check(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["playwright"].Status != "unverified" || snap.Tools["playwright"].Version != "0.0.45" {
		t.Fatalf("local package metadata %#v", snap.Tools["playwright"])
	}
	if _, err := Command(context.Background(), "playwright", nil, base, nil); err == nil {
		t.Fatal("metadata-only package became executable")
	}
}

func TestConfiguredKnownMCPPathUsesValidatedToolSelection(t *testing.T) {
	base := t.TempDir()
	node := sentinel(t, filepath.Join(base, "configured 中文 space"), "node", "node")
	m := newTestManager(t, base, "", 5*time.Second)
	snap, err := m.Check(context.Background(), Config{MCPCommands: []string{node}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["node"].Status != "ready" || snap.Tools["node"].Source != "configured" {
		t.Fatalf("known configured MCP ignored: %#v", snap.Tools["node"])
	}
	cmd, err := MCPCommand(node, []string{"explicit 中文"}, nil, base)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "explicit 中文") {
		t.Fatalf("known MCP actual %s %v", out, err)
	}
}

func TestProbeVersionNeverExposesOtherOutput(t *testing.T) {
	base := t.TempDir()
	node := sentinel(t, filepath.Join(base, "runtime", "bin"), "node", "version-sensitive")
	m := newTestManager(t, base, "", 5*time.Second)
	snap, err := m.Check(context.Background(), Config{Paths: map[string]string{"node": node}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tools["node"].Version != "1.2.3" {
		t.Fatalf("raw version leaked surrounding output: %q", snap.Tools["node"].Version)
	}
}
