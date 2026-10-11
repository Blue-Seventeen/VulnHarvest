package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("NORMA_MCP_TEST_CHILD") == "grandchild" {
		for {
			_ = os.WriteFile(os.Getenv("NORMA_MCP_HEARTBEAT"), []byte(fmt.Sprint(time.Now().UnixNano())), 0600)
			time.Sleep(10 * time.Millisecond)
		}
	}
	if os.Getenv("NORMA_MCP_TEST_CHILD") == "1" {
		if os.Getenv("NORMA_MCP_SPAWN_DESCENDANT") == "1" {
			exe, _ := os.Executable()
			child := exec.Command(exe)
			child.Env = append(os.Environ(), "NORMA_MCP_TEST_CHILD=grandchild")
			_ = child.Start()
		}
		fakeServer(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPreparedStdioCloseStopsPackageRunnerDescendant(t *testing.T) {
	exe, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "descendant-heartbeat")
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "NORMA_MCP_TEST_CHILD=1", "NORMA_MCP_SPAWN_DESCENDANT=1", "NORMA_MCP_HEARTBEAT="+marker)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := NewStdioClientWithCommand(ctx, "descendant", cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = client.Close()
	before, _ := os.ReadFile(marker)
	time.Sleep(150 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if string(before) != string(after) {
		t.Fatal("package-runner descendant kept writing after MCP Close")
	}
}

// Detects command re-resolution or attaching handshake cancellation to child lifetime.
func TestPreparedStdioRealHandshakeRetainsProcessUntilClose(t *testing.T) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "中文 space argv")
	cmd.Env = append(os.Environ(), "NORMA_MCP_TEST_CHILD=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, err := NewStdioClientWithCommand(ctx, "sentinel", cmd)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.Tools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("handshake context killed live child: %v %v", tools, err)
	}
	res, err := tools[0].Call(context.Background(), []byte(`{}`), nil)
	if err != nil || res.Flatten() != "echoed!" {
		t.Fatalf("real MCP call %v %s", err, res.Flatten())
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("Close did not Wait child")
	}
	_ = client.Close()
}

func TestPreparedStdioRejectsConflictingPipesWithoutStarting(t *testing.T) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe)
	cmd.Stdin = strings.NewReader("conflict")
	if _, err := NewStdioClientWithCommand(context.Background(), "invalid", cmd); err == nil {
		t.Fatal("accepted conflicting stdin")
	}
	if cmd.Process != nil {
		t.Fatal("invalid command started")
	}
	if _, err := NewStdioClientWithCommand(context.Background(), "nil", nil); err == nil {
		t.Fatal("accepted nil cmd")
	}
	cmd = exec.Command(exe)
	cmd.Stdout = io.Discard
	if _, err := NewStdioClientWithCommand(context.Background(), "invalid", cmd); err == nil {
		t.Fatal("accepted conflicting stdout")
	}
}
