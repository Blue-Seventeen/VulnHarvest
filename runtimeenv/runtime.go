// Package runtimeenv detects optional local tools and owns the single published
// execution snapshot. It does not start services, install tools, or persist config.
package runtimeenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/internal/toolrelay"
	tool "github.com/Autumn-27/norma/tool"
)

type Options struct {
	BaseDir  string
	StateDir string
	// HostPATH is captured before any aliases are installed; empty means no host PATH.
	HostPATH string
	// RelayExecutable must implement RunRelay before flags, locks, DB or services.
	RelayExecutable string
	// RelayDigest is required by distributions and rechecked on every Check.
	RelayDigest      string
	ProbeTimeout     time.Duration
	ProbeOutputLimit int
}

// Config contains executable paths and command names only. MCP business argv/env
// never participate in detection. Paths use python,node,npm,npx,rg,nmap,curl,shell,
// chromium,playwright keys. Relative executable paths are interpreted under BaseDir.
type Config struct {
	Paths       map[string]string
	MCPCommands []string
}

type Failure struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type ToolStatus struct {
	Name             string    `json:"name"`
	Source           string    `json:"source"`
	Version          string    `json:"version,omitempty"`
	Path             string    `json:"path,omitempty"`
	Executable       string    `json:"executable,omitempty"`
	Architecture     string    `json:"architecture,omitempty"`
	Status           string    `json:"status"`
	AffectedFeatures []string  `json:"affected_features"`
	Failures         []Failure `json:"candidate_failures,omitempty"`
}
type Snapshot struct {
	Generation   uint64                `json:"generation"`
	Tools        map[string]ToolStatus `json:"tools"`
	PTYSupported bool                  `json:"pty_supported"`
	PTYStatus    string                `json:"pty_status"`
}

type ToolManager struct {
	mu          sync.Mutex
	options     Options
	root        string
	generation  uint64
	closed      bool
	closeErr    error
	parentRoot  *os.Root
	sessionName string
}

var owner struct {
	sync.Mutex
	manager *ToolManager
}

// New reserves the process-global SDK owner. Call Check before any portable tool
// invocation. 5b must stop/join shell/PTY/background/MCP consumers before Close.
func New(options Options) (*ToolManager, error) {
	if !filepath.IsAbs(options.BaseDir) {
		return nil, fmt.Errorf("base directory must be absolute")
	}
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.BaseDir, "state")
	}
	if !filepath.IsAbs(options.StateDir) || !filepath.IsAbs(options.RelayExecutable) {
		return nil, fmt.Errorf("state and relay paths must be absolute")
	}
	if len(options.StateDir) > 32768 || strings.ContainsRune(options.StateDir, os.PathListSeparator) {
		return nil, fmt.Errorf("state path exceeds relay PATH bounds")
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 2 * time.Second
	}
	if options.ProbeOutputLimit <= 0 {
		options.ProbeOutputLimit = 16 << 10
	}
	// Hard ceilings protect callers from accidentally disabling probe bounds.
	if options.ProbeTimeout > 10*time.Second {
		options.ProbeTimeout = 10 * time.Second
	}
	if options.ProbeOutputLimit > 64<<10 {
		options.ProbeOutputLimit = 64 << 10
	}
	owner.Lock()
	defer owner.Unlock()
	if owner.manager != nil || tool.ExecutionSnapshot() != nil {
		return nil, fmt.Errorf("execution runtime already owned")
	}
	parent := filepath.Join(options.StateDir, "runtime-path")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	parentRoot, err := toolrelay.CanonicalRoot(parent)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		parentRoot.Close()
		return nil, err
	}
	sessionName := "session-" + hex.EncodeToString(nonce)
	if err = parentRoot.Mkdir(sessionName, 0700); err != nil {
		parentRoot.Close()
		return nil, err
	}
	metadata, err := parentRoot.OpenFile(filepath.Join(sessionName, "session.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = metadata.Write([]byte(`{"version":1,"shared_cache":true}`))
		err = errors.Join(err, metadata.Close())
	}
	if err != nil {
		parentRoot.RemoveAll(sessionName)
		parentRoot.Close()
		return nil, err
	}
	m := &ToolManager{options: options, root: filepath.Join(parent, sessionName), parentRoot: parentRoot, sessionName: sessionName}
	owner.manager = m
	return m, nil
}

func (m *ToolManager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}
	}
	return publishedSnapshot()
}

func publishedSnapshot() Snapshot {
	rt := tool.ExecutionSnapshot()
	var out Snapshot
	if rt != nil {
		_ = json.Unmarshal(rt.Metadata, &out)
	}
	return out
}

// Check is serialized; complete alias preparation precedes one SDK publication.
// Failure/cancellation preserves the previous usable generation. Running children
// keep immutable selections; shared cache survives session Close.
func (m *ToolManager) Check(ctx context.Context, config Config) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}, fmt.Errorf("tool manager closed")
	}
	if ctx.Err() != nil {
		return publishedSnapshot(), ctx.Err()
	}
	generation := m.generation + 1
	snap := Snapshot{Generation: generation, Tools: map[string]ToolStatus{}, PTYSupported: runtime.GOOS != "windows", PTYStatus: "supported"}
	if !snap.PTYSupported {
		snap.PTYStatus = "unsupported on Windows"
	}
	rt := &tool.ExecutionRuntime{Generation: generation, Commands: map[string]tool.LaunchSpec{}}
	knownMCPPaths := map[string][]string{}
	for _, command := range config.MCPCommands {
		if name := canonicalName(command); name != "" && filepath.Base(command) != command {
			knownMCPPaths[name] = append(knownMCPPaths[name], command)
		}
	}
	for _, def := range definitions() {
		status, spec := m.detect(ctx, def, config.Paths[def.name], rt, knownMCPPaths[def.name]...)
		snap.Tools[def.name] = status
		if spec.Executable != "" {
			rt.Commands[def.name] = spec
		}
	}
	if shell, ok := rt.Commands["shell"]; ok {
		rt.ShellPath = shell.Executable
		rt.ShellArgs = []string{"-c"}
		if runtime.GOOS == "windows" {
			rt.ShellArgs = []string{"-NoProfile", "-NonInteractive", "-Command"}
		}
	}
	// No custom command is probed or started. Only file metadata is inspected.
	commands := append([]string(nil), config.MCPCommands...)
	sort.Strings(commands)
	for _, command := range commands {
		if command == "" {
			continue
		}
		if canonical := canonicalName(command); canonical != "" {
			// A known command with no path follows the selected known-tool snapshot.
			if filepath.Base(command) == command {
				continue
			}
		}
		def := toolDefinition{name: "mcp:" + command, names: []string{filepath.Base(command)}, features: []string{"configured MCP"}}
		status, spec := m.detect(ctx, def, command, rt)
		snap.Tools[def.name] = status
		if spec.Executable != "" {
			rt.Commands[command] = spec
		}
	}
	if ctx.Err() != nil {
		return publishedSnapshot(), ctx.Err()
	}
	aliasPATH, err := m.prepareAliases(ctx, rt)
	if err != nil {
		return publishedSnapshot(), err
	}
	rt.ChildPATH = aliasPATH
	if host := m.filteredPATH(); host != "" {
		rt.ChildPATH += string(os.PathListSeparator) + host
	}
	rt.Metadata, _ = json.Marshal(snap)
	if ctx.Err() != nil {
		return publishedSnapshot(), ctx.Err()
	}
	if err := tool.SetExecutionRuntime(rt); err != nil {
		return publishedSnapshot(), err
	}
	m.generation = generation
	return snap, nil
}

// Close is idempotent and clears only this manager's SDK ownership. Consumers
// must already be joined. Shared immutable cache is intentionally retained.
func (m *ToolManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	owner.Lock()
	defer owner.Unlock()
	var err error
	if owner.manager == m {
		err = tool.SetExecutionRuntime(nil)
		owner.manager = nil
	}
	removeErr := m.parentRoot.RemoveAll(m.sessionName)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.GOOS == "windows" && removeErr != nil && (errors.Is(removeErr, syscall.Errno(5)) || errors.Is(removeErr, syscall.Errno(32))) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		removeErr = m.parentRoot.RemoveAll(m.sessionName)
	}
	m.closeErr = errors.Join(err, removeErr, m.parentRoot.Close())
	return m.closeErr
}

func removeAliases(path string) error {
	err := os.RemoveAll(path)
	if runtime.GOOS != "windows" {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for err != nil && (errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		err = os.RemoveAll(path)
	}
	return err
}

type toolDefinition struct {
	name     string
	names    []string
	probe    []string
	features []string
}

func definitions() []toolDefinition {
	shellNames := []string{"bash"}
	shellProbe := []string{"--version"}
	if runtime.GOOS == "windows" {
		shellNames = []string{"powershell", "pwsh"}
		shellProbe = []string{"-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()"}
	}
	return []toolDefinition{
		{"node", []string{"node"}, []string{"--version"}, []string{"Node scripts", "npm/npx", "MCP"}},
		{"python", []string{"python3", "python"}, []string{"--version"}, []string{"Python custom tools"}},
		{"npm", []string{"npm"}, []string{"--version"}, []string{"explicit npm commands"}},
		{"npx", []string{"npx"}, []string{"--version"}, []string{"explicit MCP packages"}},
		{"rg", []string{"rg"}, []string{"--version"}, []string{"fast grep (Go fallback available)"}},
		{"nmap", []string{"nmap"}, []string{"--version"}, []string{"network scans"}},
		{"curl", []string{"curl"}, []string{"--version"}, []string{"HTTP shell commands"}},
		{"shell", shellNames, shellProbe, []string{"foreground/background scripts", "PTY on supported OS"}},
		{"chromium", []string{"chromium", "chromium-browser", "google-chrome", "chrome"}, nil, []string{"browser automation (execution unverified)"}},
		{"playwright", nil, nil, []string{"browser MCP (local package metadata only)"}},
	}
}

type candidate struct{ path, source string }

func (m *ToolManager) candidates(def toolDefinition, configured string, extra ...string) []candidate {
	bundle := filepath.Join(m.options.BaseDir, "runtime", "bin")
	var out []candidate
	for _, name := range def.names {
		for _, path := range executablePaths(bundle, name) {
			out = append(out, candidate{path, "bundled"})
		}
	}
	for _, configured := range append([]string{configured}, extra...) {
		if configured == "" {
			continue
		}
		p := configured
		if !filepath.IsAbs(p) {
			p = filepath.Join(m.options.BaseDir, p)
		}
		out = append(out, candidate{filepath.Clean(p), "configured"})
	}
	for _, dir := range filepath.SplitList(m.filteredPATH()) {
		for _, name := range def.names {
			for _, path := range executablePaths(dir, name) {
				out = append(out, candidate{path, "path"})
			}
		}
	}
	seen := map[string]bool{}
	result := out[:0]
	for _, c := range out {
		key := pathKey(c.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, c)
	}
	return result
}

func executablePaths(dir, name string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(name) != "" {
		return []string{filepath.Join(dir, name)}
	}
	return []string{filepath.Join(dir, name+".exe"), filepath.Join(dir, name+".cmd"), filepath.Join(dir, name+".bat")}
}

func pathKey(path string) string {
	p := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}
func within(dir, path string) bool {
	r, err := filepath.Rel(pathKey(dir), pathKey(path))
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}
func (m *ToolManager) filteredPATH() string {
	var dirs []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(m.options.HostPATH) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		key := pathKey(dir)
		if seen[key] || within(filepath.Join(m.options.BaseDir, "runtime", "bin"), dir) || within(filepath.Join(m.options.StateDir, "runtime-path"), dir) {
			continue
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func canonicalName(name string) string {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd"), ".bat")
	switch base {
	case "python", "python3":
		return "python"
	case "node", "npm", "npx", "rg", "nmap", "curl":
		return base
	case "bash", "powershell", "pwsh":
		return "shell"
	}
	return ""
}
