// Command portablebuild rebuilds the production UI and creates portable archives.
// It runs on the build host; the resulting packages need no Go or Node runtime.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/Autumn-27/artex/internal/toolrelay"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const version = "v0.0.1"

var targets = []string{"windows/amd64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	release := flag.Bool("release", false, "build all five supported platforms")
	target := flag.String("target", runtime.GOOS+"/"+runtime.GOARCH, "OS/ARCH")
	output := flag.String("output-dir", "dist", "archive directory")
	flag.Parse()
	selected := []string{*target}
	if *release {
		selected = targets
	}
	for _, t := range selected {
		if !validTarget(t) {
			return fmt.Errorf("unsupported target %q", t)
		}
	}
	source, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(source, "go.mod")); err != nil {
		return fmt.Errorf("run portablebuild from the repository root: %w", err)
	}
	// Always rebuild: external .env files must not bake mocks or workstation ports
	// into a portable package. Invoking next directly also works on Windows.
	if err := command(source, environment(map[string]string{"NEXT_EXPORT": "1", "NEXT_PUBLIC_MOCK": "0", "NEXT_PUBLIC_SSE_BASE": ""}), "node", "web/node_modules/next/dist/bin/next", "build", "web"); err != nil {
		return err
	}
	dist := filepath.Join(source, "server", "webui", "dist")
	if err := os.RemoveAll(dist); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(source, "web", "out"), dist); err != nil {
		return err
	}
	buildRoot, err := os.MkdirTemp("", "vulnharvest-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildRoot)
	var checksums strings.Builder
	for _, t := range selected {
		parts := strings.Split(t, "/")
		binary := filepath.Join(buildRoot, "artex")
		if parts[0] == "windows" {
			binary += ".exe"
		}
		fmt.Println("Build VulnHarvest", version, t)
		env := environment(map[string]string{"GOOS": parts[0], "GOARCH": parts[1], "CGO_ENABLED": "0"})
		helper := filepath.Join(buildRoot, "vulnharvest-tool-relay")
		if parts[0] == "windows" {
			helper += ".exe"
		}
		if err := command(source, env, "go", "build", "-trimpath", "-ldflags", "-s -w -buildid=", "-o", helper, "./cmd/toolrelay"); err != nil {
			return err
		}
		helperBytes, err := os.ReadFile(helper)
		if err != nil {
			return err
		}
		digest := toolrelay.Hash(helperBytes)
		if err := toolrelay.VerifyAsset(helper, digest, parts[0], parts[1]); err != nil {
			return err
		}
		if err := command(source, env, "go", "build", "-tags", "embedui", "-trimpath", "-ldflags", "-s -w -buildid= -X main.version="+version+" -X main.relayDigest="+digest, "-o", binary, "./cmd/artex"); err != nil {
			return err
		}
		archive, err := packageTarget(source, *output, binary, parts[0], parts[1])
		if err != nil {
			return err
		}
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), filepath.Base(archive))
		fmt.Println("Archive:", archive)
	}
	return os.WriteFile(filepath.Join(*output, "SHA256SUMS"), []byte(checksums.String()), 0644)
}
func validTarget(target string) bool {
	for _, t := range targets {
		if target == t {
			return true
		}
	}
	return false
}
func environment(values map[string]string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		key := strings.SplitN(v, "=", 2)[0]
		keep := true
		for k := range values {
			if strings.EqualFold(k, key) {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, v)
		}
	}
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env
}
func command(dir string, env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = env
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(destination, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected: %s", path)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func packageTarget(source, output, binary, goos, goarch string) (archivePath string, err error) {
	if !validTarget(goos + "/" + goarch) {
		return "", fmt.Errorf("unsupported target")
	}
	name := "VulnHarvest-" + version + "-" + goos + "-" + goarch
	if err = os.MkdirAll(output, 0755); err != nil {
		return "", err
	}
	archivePath = filepath.Join(output, name+".zip")
	file, err := os.CreateTemp(output, ".vulnharvest-package-*")
	if err != nil {
		return "", err
	}
	tmp := file.Name()
	defer func() { file.Close(); os.Remove(tmp) }()
	zw := zip.NewWriter(file)
	addFile := func(src, dst string, mode os.FileMode) error {
		info, e := os.Lstat(src)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular package input %s", src)
		}
		in, e := os.Open(src)
		if e != nil {
			return e
		}
		defer in.Close()
		h := &zip.FileHeader{Name: name + "/" + filepath.ToSlash(dst), Method: zip.Deflate}
		h.SetMode(mode)
		h.SetModTime(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
		out, e := zw.CreateHeader(h)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		return e
	}
	binName := "artex"
	launcher := "start.sh"
	if goos == "windows" {
		binName = "artex.exe"
		launcher = "start.bat"
	}
	helperName := "vulnharvest-tool-relay"
	if goos == "windows" {
		helperName += ".exe"
	}
	helperPath := filepath.Join(filepath.Dir(binary), helperName)
	helperBytes, e := os.ReadFile(helperPath)
	if e != nil {
		return "", fmt.Errorf("required relay asset: %w", e)
	}
	if e = toolrelay.VerifyAsset(helperPath, toolrelay.Hash(helperBytes), goos, goarch); e != nil {
		return "", e
	}
	files := []struct {
		src, dst string
		mode     os.FileMode
	}{
		{binary, binName, 0755}, {filepath.Join(source, "portable", launcher), launcher, 0755},
		{helperPath, "runtime/bin/" + helperName, 0755},
		{filepath.Join(source, "config.example.json"), "config.example.json", 0644},
		{filepath.Join(source, "README.md"), "README.md", 0644}, {filepath.Join(source, "LICENSE"), "LICENSE", 0644},
		{filepath.Join(source, "web", "LICENSE"), "licenses/web-MIT.txt", 0644},
		{filepath.Join(source, "third_party", "go-mitmproxy", "LICENSE"), "licenses/go-mitmproxy-MIT.txt", 0644},
		{filepath.Join(source, "portable", "THIRD-PARTY-NOTICES.md"), "THIRD-PARTY-NOTICES.md", 0644},
	}
	for _, p := range files {
		if err = addFile(p.src, p.dst, p.mode); err != nil {
			return "", err
		}
	}
	// Exact checked-in skill whitelist includes references and scripts, never
	// local caches, custom secrets or untracked workspace content.
	manifest, e := os.ReadFile(filepath.Join(source, "portable", "skills-manifest.txt"))
	if e != nil {
		return "", e
	}
	for _, rel := range strings.Split(string(manifest), "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" || strings.HasPrefix(rel, "#") {
			continue
		}
		if !strings.HasPrefix(rel, "skills/") || strings.Contains(rel, "\\") || strings.Contains(rel, "../") || filepath.IsAbs(rel) {
			return "", fmt.Errorf("invalid skill manifest path %q", rel)
		}
		if err = addFile(filepath.Join(source, filepath.FromSlash(rel)), rel, 0755); err != nil {
			return "", err
		}
	}
	h := &zip.FileHeader{Name: name + "/runtime/bin/"}
	h.SetMode(os.ModeDir | 0755)
	if _, err = zw.CreateHeader(h); err != nil {
		return "", err
	}
	if err = zw.Close(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, archivePath); err != nil {
		return "", err
	}
	return archivePath, nil
}
