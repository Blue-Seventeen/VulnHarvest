package runtimeenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	tool "github.com/Autumn-27/norma/tool"
)

func (m *ToolManager) detect(ctx context.Context, def toolDefinition, configured string, rt *tool.ExecutionRuntime, extra ...string) (ToolStatus, tool.LaunchSpec) {
	status := ToolStatus{Name: def.name, Status: "missing", AffectedFeatures: def.features}
	candidates := m.candidates(def, configured, extra...)
	if def.name == "playwright" {
		candidates = m.playwrightCandidates(configured, rt)
	}
	for _, c := range candidates {
		st, err := os.Stat(c.path)
		if err != nil {
			if c.source == "configured" {
				status.Failures = append(status.Failures, Failure{c.source, c.path, "configured file unavailable"})
			}
			continue
		}
		if st.IsDir() {
			status.Failures = append(status.Failures, Failure{c.source, c.path, "not an executable file"})
			continue
		}
		if def.name == "playwright" {
			version, err := packageVersion(c.path)
			if err != nil {
				status.Failures = append(status.Failures, Failure{c.source, c.path, "local Playwright package metadata invalid"})
				continue
			}
			status.Source, status.Path, status.Status, status.Version = c.source, c.path, "unverified", version
			return status, tool.LaunchSpec{}
		}
		if runtime.GOOS != "windows" && st.Mode()&0111 == 0 {
			status.Failures = append(status.Failures, Failure{c.source, c.path, "file lacks executable permission"})
			continue
		}
		spec, reason := resolveLaunch(c.path, def.name, rt)
		if reason != "" {
			status.Failures = append(status.Failures, Failure{c.source, c.path, reason})
			continue
		}
		if len(def.probe) == 0 {
			status.Source, status.Path, status.Executable, status.Status = c.source, c.path, spec.Executable, "unverified"
			status.Architecture, status.Version = fileMetadata(c.path)
			return status, spec
		}
		version, reason := m.probe(ctx, spec, def.probe)
		if reason != "" {
			status.Failures = append(status.Failures, Failure{c.source, c.path, reason})
			continue
		}
		status.Source, status.Path, status.Executable, status.Status, status.Version = c.source, c.path, spec.Executable, "ready", version
		return status, spec
	}
	return status, tool.LaunchSpec{}
}

func resolveLaunch(path, name string, rt *tool.ExecutionRuntime) (tool.LaunchSpec, string) {
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".cmd" || ext == ".bat" {
			if name != "npm" && name != "npx" {
				return tool.LaunchSpec{}, "batch wrapper execution unverified; supply native executable/local Node entrypoint"
			}
			node, ok := rt.Commands["node"]
			if !ok {
				return tool.LaunchSpec{}, "known npm wrapper requires selected Node"
			}
			cli := filepath.Join(filepath.Dir(path), "node_modules", "npm", "bin", name+"-cli.js")
			st, err := os.Stat(cli)
			if err != nil || st.IsDir() {
				return tool.LaunchSpec{}, "known npm Node CLI layout unresolved"
			}
			return tool.LaunchSpec{Executable: node.Executable, PrefixArgs: []string{cli}}, ""
		}
	}
	return tool.LaunchSpec{Executable: path}, ""
}

type boundedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := w.limit - w.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
		w.overflow = true
		w.cancel()
	}
	_, _ = w.buf.Write(p)
	return n, nil
}

func (m *ToolManager) probe(ctx context.Context, spec tool.LaunchSpec, args []string) (string, string) {
	probeCtx, cancel := context.WithTimeout(ctx, m.options.ProbeTimeout)
	defer cancel()
	cmd := exec.Command(spec.Executable, append(append([]string(nil), spec.PrefixArgs...), args...)...)
	cmd.Env = tool.RuntimeEnv(os.Environ(), nil, m.filteredPATH())
	out := &boundedOutput{limit: m.options.ProbeOutputLimit, cancel: cancel}
	err, cleanupErr := runOwnedProbe(probeCtx, cmd, out)
	if cleanupErr != nil {
		return "", "version probe process cleanup failed"
	}
	if out.overflow {
		return "", "version probe output limit exceeded"
	}
	if probeCtx.Err() != nil {
		return "", "version probe timeout/cancellation"
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", "version probe unsuccessful exit"
		}
		return "", "version probe could not start (format/permission/loader or wrapper error)"
	}
	text := strings.TrimSpace(out.buf.String())
	if text == "" {
		return "", "version probe returned no version"
	}
	version := versionPattern.FindString(text)
	if version == "" {
		return "", "version probe output not recognized"
	}
	return version, ""
}

var versionPattern = regexp.MustCompile(`\b[0-9]+\.[0-9]+(?:\.[0-9]+)*(?:[-+][A-Za-z0-9.-]+)?`)

func (m *ToolManager) playwrightCandidates(configured string, rt *tool.ExecutionRuntime) []candidate {
	var out []candidate
	add := func(dir, source string) {
		for _, pkg := range []string{"playwright", "playwright-core", "@playwright/mcp"} {
			out = append(out, candidate{filepath.Join(dir, "node_modules", filepath.FromSlash(pkg), "package.json"), source})
		}
	}
	add(filepath.Join(m.options.BaseDir, "runtime"), "bundled")
	add(filepath.Join(m.options.BaseDir, "runtime", "bin"), "bundled")
	if configured != "" {
		path := configured
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.options.BaseDir, path)
		}
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			path = filepath.Join(path, "package.json")
		}
		out = append(out, candidate{path, "configured"})
	}
	if node, ok := rt.Commands["node"]; ok {
		add(filepath.Dir(node.Executable), "path")
	}
	for _, dir := range filepath.SplitList(m.filteredPATH()) {
		add(dir, "path")
	}
	seen := map[string]bool{}
	unique := out[:0]
	for _, c := range out {
		key := pathKey(c.path)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, c)
		}
	}
	return unique
}

func packageVersion(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&pkg); err != nil {
		return "", err
	}
	if pkg.Name != "playwright" && pkg.Name != "playwright-core" && pkg.Name != "@playwright/mcp" {
		return "", fmt.Errorf("unknown package")
	}
	if versionPattern.FindString(pkg.Version) != pkg.Version || pkg.Version == "" {
		return "", fmt.Errorf("invalid version")
	}
	return pkg.Version, nil
}
