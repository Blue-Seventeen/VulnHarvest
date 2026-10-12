package runtimeenv

import (
	"bufio"
	"bytes"
	"context"
	tool "github.com/Autumn-27/norma/tool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnchangedChecksReuseImmutableCacheAcrossClose(t *testing.T) {
	base := t.TempDir()
	m := newTestManager(t, base, "", time.Second)
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	first := tool.ExecutionSnapshot().ChildPATH
	for i := 0; i < 20; i++ {
		if _, err := m.Check(context.Background(), Config{}); err != nil {
			t.Fatal(err)
		}
		if tool.ExecutionSnapshot().ChildPATH != first {
			t.Fatal("unchanged check allocated another selection")
		}
	}
	if m.Snapshot().Generation != 21 {
		t.Fatal("fresh detection generation lost")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.root); !os.IsNotExist(err) {
		t.Fatal("session metadata retained")
	}
	m = newTestManager(t, base, "", time.Second)
	if _, err := m.Check(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}
	if tool.ExecutionSnapshot().ChildPATH != first {
		t.Fatal("restart allocated another cache")
	}
	entries, err := os.ReadDir(filepath.Join(base, "独立 state", "runtime-path", "cache"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("helper cache %v %v", entries, err)
	}
}

func TestOldConsumerExecutesCapturedChoiceAfterPublicationAndClose(t *testing.T) {
	base := t.TempDir()
	oldNode := sentinel(t, filepath.Join(base, "old"), "node", "old")
	newNode := sentinel(t, filepath.Join(base, "new"), "node", "new")
	waiter := sentinel(t, base, "waiting-consumer", "delayed-relay")
	m := newTestManager(t, base, "", 5*time.Second)
	if _, e := m.Check(context.Background(), Config{Paths: map[string]string{"node": oldNode}}); e != nil {
		t.Fatal(e)
	}
	captured := tool.ExecutionSnapshot().ChildPATH
	c := exec.Command(waiter)
	c.Env = tool.RuntimeEnv(os.Environ(), nil, captured)
	in, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if c.ProcessState == nil {
			c.Process.Kill()
			c.Wait()
		}
	})
	reader := bufio.NewReader(out)
	line, e := reader.ReadString('\n')
	if e != nil || line != "ready\n" {
		t.Fatalf("consumer %s %v", line, e)
	}
	if _, e = m.Check(context.Background(), Config{Paths: map[string]string{"node": newNode}}); e != nil {
		t.Fatal(e)
	}
	if tool.ExecutionSnapshot().ChildPATH == captured {
		t.Fatal("new selection reused old choice")
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = in.Write([]byte("release\n")); e != nil {
		t.Fatal(e)
	}
	in.Close()
	receipt, e := reader.ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Wait(); e != nil {
		t.Fatalf("old consumer %v %s", e, stderr.String())
	}
	if !strings.Contains(receipt, hostNodeJSON(oldNode)) || strings.Contains(receipt, hostNodeJSON(newNode)) || !strings.Contains(receipt, "中文 arg") || !strings.Contains(receipt, "space arg") {
		t.Fatalf("old receipt %s", receipt)
	}
}

func TestRelayExactPrefixStdioExitAndFailClosed(t *testing.T) {
	base := t.TempDir()
	target := sentinel(t, base, "stdio-target", "stdio")
	m := newTestManager(t, base, "", time.Second)
	rt := &tool.ExecutionRuntime{Commands: map[string]tool.LaunchSpec{"node": {Executable: target, PrefixArgs: []string{"prefix 中文", "prefix space"}}}}
	path, e := m.prepareAliases(context.Background(), rt)
	if e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(filepath.SplitList(path)[1], "node")
	if filepath.Ext(target) == ".exe" {
		alias += ".exe"
	}
	c := exec.Command(alias, "中文 arg", "space arg")
	c.Env = tool.RuntimeEnv(os.Environ(), nil, path)
	c.Stdin = strings.NewReader("stdin 中文\n")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 || stderr.String() != "exact-stderr" || !strings.Contains(string(b), `"args":["prefix 中文","prefix space","中文 arg","space arg"]`) || !strings.Contains(string(b), `"stdin":"stdin 中文\n"`) {
		t.Fatalf("stdio %s stderr=%s exit=%v", b, stderr.String(), e)
	}
	missing := filepath.Join(filepath.Dir(alias), "rg"+filepath.Ext(alias))
	c = exec.Command(missing)
	c.Env = tool.RuntimeEnv(os.Environ(), nil, path)
	e = c.Run()
	exit, ok = e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 127 {
		t.Fatalf("missing exit %v", e)
	}
	c = exec.Command(alias)
	c.Env = tool.RuntimeEnv(os.Environ(), nil, filepath.Dir(alias))
	e = c.Run()
	exit, ok = e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 126 {
		t.Fatalf("invalid selection exit %v", e)
	}
}
