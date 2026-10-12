package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageWhitelistPreservesSkillsAndExcludesRuntimeState(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	for name, body := range map[string]string{"skills/demo/SKILL.md": "skill", "skills/demo/references/ref.md": "reference", "skills/demo/scripts/run.py": "script", "skills/demo/scripts/__pycache__/secret.pyc": "secret", "data/key": "secret", "state/db": "secret", "config.json": "secret", "config.example.json": "{}", "README.md": "VulnHarvest", "LICENSE": "AGPL", "web/LICENSE": "MIT", "third_party/go-mitmproxy/LICENSE": "MIT", "portable/THIRD-PARTY-NOTICES.md": "notice", "portable/start.sh": "launcher", "portable/start.bat": "launcher", "portable/skills-manifest.txt": "skills/demo/SKILL.md\nskills/demo/references/ref.md\nskills/demo/scripts/run.py\n"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte(body), 0600)
	}
	binary := filepath.Join(root, "artex.exe")
	os.WriteFile(binary, []byte("binary"), 0700)
	archive, err := packageTarget(root, out, binary, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
		if strings.Contains(f.Name, "secret") || strings.Contains(f.Name, "__pycache__") || strings.Contains(f.Name, "/data/") || strings.Contains(f.Name, "/state/") || strings.HasSuffix(f.Name, "/config.json") {
			t.Fatalf("private artifact in package: %s", f.Name)
		}
	}
	prefix := "VulnHarvest-v0.0.1-windows-amd64/"
	for _, name := range []string{"artex.exe", "start.bat", "config.example.json", "LICENSE", "licenses/web-MIT.txt", "licenses/go-mitmproxy-MIT.txt", "THIRD-PARTY-NOTICES.md", "skills/demo/references/ref.md", "skills/demo/scripts/run.py", "runtime/bin/"} {
		if !names[prefix+name] {
			t.Errorf("missing %s", name)
		}
	}
}
