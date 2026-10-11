package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// stdioCloser shuts down a spawned MCP server process and its pipes.
type stdioCloser struct {
	cmd   *exec.Cmd
	stdin io.Closer
	once  sync.Once
}

func (s *stdioCloser) Close() error {
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			stopStdioProcess(s.cmd)
			_ = s.cmd.Wait()
		}
		if s.stdin != nil {
			s.stdin.Close()
		}
	})
	return nil
}

// NewStdioClient spawns an MCP server as a subprocess, performs the handshake,
// and returns a connected Client (FR-10.1). Call Close to terminate it.
//
// env holds extra environment variables (e.g. API tokens an MCP server needs)
// applied on top of the parent process environment; pass nil for none.
func NewStdioClient(ctx context.Context, server, command string, env map[string]string, args ...string) (*Client, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return NewStdioClientWithCommand(ctx, server, cmd)
}

// NewStdioClientWithCommand owns an unstarted prepared command. ctx bounds only
// initialization; use exec.Command (not exec.CommandContext with handshake ctx)
// when the session must outlive discovery. Close kills and waits the child.
func NewStdioClientWithCommand(ctx context.Context, server string, cmd *exec.Cmd) (*Client, error) {
	if cmd == nil || cmd.Process != nil || cmd.Stdin != nil || cmd.Stdout != nil {
		return nil, fmt.Errorf("MCP requires unstarted command with unused stdio pipes")
	}
	if err := prepareStdioProcess(cmd); err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	c := &Client{
		server: server,
		conn:   newConn(stdout, stdin),
		closer: &stdioCloser{cmd: cmd, stdin: stdin},
	}
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// newClientWithConn builds a Client over an arbitrary transport (used by tests).
func newClientWithConn(ctx context.Context, server string, r io.Reader, w io.Writer, closer io.Closer) (*Client, error) {
	c := &Client{server: server, conn: newConn(r, w), closer: closer}
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
