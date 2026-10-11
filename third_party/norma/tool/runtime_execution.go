package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// LaunchSpec preserves argv boundaries, including known Node CLI wrappers.
type LaunchSpec struct {
	Executable string   `json:"executable"`
	PrefixArgs []string `json:"prefix_args,omitempty"`
}

// ExecutionRuntime is one immutable process-wide selection. Its sole host owner
// must publish before tool use and stop/join consumers before clearing it. Separate
// Managers cannot own different selections in the same process. Running sessions
// retain their generation's PATH; publishers must keep old alias directories alive.
type ExecutionRuntime struct {
	Generation uint64
	ShellPath  string
	ShellArgs  []string
	ChildPATH  string
	Commands   map[string]LaunchSpec
	Metadata   json.RawMessage
}

var executionRuntime atomic.Pointer[ExecutionRuntime]

func cloneRuntime(rt *ExecutionRuntime) *ExecutionRuntime {
	if rt == nil {
		return nil
	}
	out := *rt
	out.ShellArgs = append([]string(nil), rt.ShellArgs...)
	out.Metadata = append(json.RawMessage(nil), rt.Metadata...)
	out.Commands = make(map[string]LaunchSpec, len(rt.Commands))
	for k, v := range rt.Commands {
		v.PrefixArgs = append([]string(nil), v.PrefixArgs...)
		out.Commands[k] = v
	}
	return &out
}

// ExecutionSnapshot returns an independent copy of the single published value.
func ExecutionSnapshot() *ExecutionRuntime { return cloneRuntime(executionRuntime.Load()) }

// SetExecutionRuntime validates before publishing; failures preserve the old value.
// nil restores legacy execution. This setter performs no probes or installation.
func SetExecutionRuntime(rt *ExecutionRuntime) error {
	next := cloneRuntime(rt)
	if next != nil {
		if next.ShellPath != "" && !filepath.IsAbs(next.ShellPath) {
			return fmt.Errorf("shell path must be absolute")
		}
		for _, spec := range next.Commands {
			if spec.Executable == "" || !filepath.IsAbs(spec.Executable) {
				return fmt.Errorf("tool path must be absolute")
			}
		}
	}
	executionRuntime.Store(next)
	return nil
}

// RuntimeEnv merges extra values and overrides any spelling of PATH on Windows.
func RuntimeEnv(parent, extra []string, path string) []string {
	result := make([]string, 0, len(parent)+len(extra)+1)
	seen := map[string]bool{}
	all := append(append([]string(nil), parent...), extra...)
	all = append(all, "PATH="+path)
	for i := len(all) - 1; i >= 0; i-- {
		k, _, ok := strings.Cut(all[i], "=")
		if !ok {
			continue
		}
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		if !seen[k] {
			seen[k] = true
			result = append(result, all[i])
		}
	}
	return result
}

// CommandFromSnapshot never resolves a name through the ambient parent PATH.
// Callers may supply one captured snapshot to keep multi-stage operations coherent.
func CommandFromSnapshot(ctx context.Context, rt *ExecutionRuntime, name string, args []string, dir string, extra []string) (*exec.Cmd, error) {
	if rt == nil {
		return nil, fmt.Errorf("execution snapshot required")
	}
	spec, ok := rt.Commands[name]
	if !ok {
		return nil, fmt.Errorf("tool %q unavailable", name)
	}
	argv := append(append([]string(nil), spec.PrefixArgs...), args...)
	cmd := exec.CommandContext(ctx, spec.Executable, argv...)
	cmd.Dir = dir
	cmd.Env = RuntimeEnv(os.Environ(), extra, rt.ChildPATH)
	prepareRuntimeCancellation(cmd)
	return cmd, nil
}

func prepareRuntimeCancellation(cmd *exec.Cmd) {
	setDetached(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			treeKill(cmd.Process)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

func newShellCommand(ctx context.Context, command, dir string, extra []string) (*exec.Cmd, error) {
	rt := executionRuntime.Load()
	if rt == nil {
		shell, flags := shellCmd()
		cmd := exec.CommandContext(ctx, shell, append(flags, command)...)
		cmd.Dir = dir
		cmd.Env = withEnv(extra)
		return cmd, nil
	}
	if rt.ShellPath == "" {
		return nil, fmt.Errorf("command interpreter unavailable")
	}
	// PowerShell's built-in curl alias otherwise bypasses native PATH selection.
	if runtime.GOOS == "windows" && len(rt.ShellArgs) > 0 && strings.EqualFold(rt.ShellArgs[len(rt.ShellArgs)-1], "-Command") {
		command = "Remove-Item Alias:curl -ErrorAction SilentlyContinue; " + command
	}
	cmd := exec.CommandContext(ctx, rt.ShellPath, append(append([]string(nil), rt.ShellArgs...), command)...)
	cmd.Dir = dir
	cmd.Env = RuntimeEnv(os.Environ(), extra, rt.ChildPATH)
	return cmd, nil
}
