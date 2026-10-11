package runtimeenv

import (
	"context"
	"fmt"
	"os/exec"

	tool "github.com/Autumn-27/norma/tool"
)

// Command uses the currently published generation and an absolute selection.
func Command(ctx context.Context, name string, args []string, dir string, env []string) (*exec.Cmd, error) {
	return tool.CommandFromSnapshot(ctx, tool.ExecutionSnapshot(), name, args, dir, env)
}

// MCPCommand builds an unstarted explicit user launch. Its lifetime is owned by
// the MCP Client, not the short discovery/handshake context. Dynamic proxy/CA
// overlays belong in env and browser argv at this call, never persisted by us.
// Explicit npx/npm package argv remain supported; Check never executes them.
func MCPCommand(command string, args []string, env map[string]string, dir string) (*exec.Cmd, error) {
	rt := tool.ExecutionSnapshot()
	if rt == nil {
		return nil, fmt.Errorf("execution snapshot required")
	}
	name := command
	// Known tool paths (including stale saved paths) follow this generation's
	// validated selection; custom command paths use the metadata-only selection.
	if canonical := canonicalName(command); canonical != "" {
		name = canonical
	}
	if _, ok := rt.Commands[name]; !ok {
		return nil, fmt.Errorf("configured MCP command unavailable; rescan required")
	}
	var extra []string
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	cmd, err := tool.CommandFromSnapshot(context.Background(), rt, name, args, dir, extra)
	if err != nil {
		return nil, err
	}
	return cmd, nil
}
