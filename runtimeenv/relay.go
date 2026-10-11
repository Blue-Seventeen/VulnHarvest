package runtimeenv

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tool "github.com/Autumn-27/norma/tool"
)

const relayManifest = ".tool-relay.json"

type relayEntry struct {
	Executable string   `json:"executable,omitempty"`
	PrefixArgs []string `json:"prefix_args,omitempty"`
}

func (m *ToolManager) prepareAliases(dir string, rt *tool.ExecutionRuntime) error {
	source, err := os.Open(m.options.RelayExecutable)
	if err != nil {
		return fmt.Errorf("relay executable unavailable")
	}
	_ = source.Close()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	manifest := map[string]relayEntry{}
	aliases := map[string]string{"node": "node", "python": "python", "python3": "python", "npm": "npm", "npx": "npx", "rg": "rg", "nmap": "nmap", "curl": "curl"}
	// An unavailable known tool still gets a failing alias. A rejected host or
	// bundled file must never become executable again through the shell's PATH.
	for alias, name := range aliases {
		spec := rt.Commands[name]
		manifest[alias] = relayEntry{spec.Executable, spec.PrefixArgs}
		target := filepath.Join(dir, alias)
		if runtime.GOOS == "windows" {
			target += ".exe"
		}
		// Windows image handles lock even unused hardlinks against deletion.
		if runtime.GOOS == "windows" {
			if err := copyExecutable(m.options.RelayExecutable, target); err != nil {
				return fmt.Errorf("create tool relay: %w", err)
			}
		} else if err := os.Link(m.options.RelayExecutable, target); err != nil {
			if err := copyExecutable(m.options.RelayExecutable, target); err != nil {
				return fmt.Errorf("create tool relay: %w", err)
			}
		}
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, relayManifest), b, 0600)
}

func copyExecutable(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// RunRelay must be the first application action. Only adjacent controlled relay
// manifests trigger dispatch. Returns (false,0) for the normal application.
// It forwards exact argv/stdin/stdout/stderr and exit status; no DB/config is read.
func RunRelay() (bool, int) {
	// os.Executable resolves symlinks on some platforms; aliases are hardlinks or
	// copies, preserving their invocation directory without changing target tools.
	exe, err := os.Executable()
	if err != nil {
		return false, 0
	}
	name := strings.TrimSuffix(filepath.Base(exe), ".exe")
	switch name {
	case "node", "python", "python3", "npm", "npx", "rg", "nmap", "curl":
	default:
		return false, 0
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), relayManifest))
	if os.IsNotExist(err) {
		return false, 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool relay manifest unavailable")
		return true, 126
	}
	var entries map[string]relayEntry
	if json.Unmarshal(b, &entries) != nil {
		fmt.Fprintln(os.Stderr, "invalid tool relay manifest")
		return true, 126
	}
	entry, ok := entries[name]
	if !ok || entry.Executable == "" {
		fmt.Fprintf(os.Stderr, "tool %s unavailable in this runtime generation\n", name)
		return true, 127
	}
	if !filepath.IsAbs(entry.Executable) {
		fmt.Fprintln(os.Stderr, "tool relay target must be absolute")
		return true, 126
	}
	argv := append(append([]string(nil), entry.PrefixArgs...), os.Args[1:]...)
	return true, relayExec(entry.Executable, argv)
}
