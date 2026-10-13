package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Each child has a real executable directory, distinct CWD and mode globals.
func TestConfigPathActualProcess(t *testing.T) {
	if os.Getenv("ARTEX_PATH_CHILD") == "1" {
		SetRuntimeMode(os.Getenv("ARTEX_PATH_MODE"))
		if os.Getenv("ARTEX_PATH_INFER") == "1" {
			mode, err := ResolveMode("")
			if err != nil || mode != "portable" {
				t.Fatalf("ResolveMode=%q err=%v", mode, err)
			}
			SetRuntimeMode(mode)
			if node := Load().Tools["node"]; node != "cwd-node" {
				t.Errorf("resolved mode=%s config=%q node=%q want cwd-node", mode, Path(), node)
			}
		}
		if got, want := Path(), os.Getenv("ARTEX_PATH_WANT_CONFIG"); got != want {
			t.Fatalf("Path=%q want %q", got, want)
		}
		if got, want := SkillPath(), os.Getenv("ARTEX_PATH_WANT_SKILLS"); got != want {
			t.Fatalf("SkillPath=%q want %q", got, want)
		}
		if os.Getenv("ARTEX_PATH_INFER") == "1" {
			if err := os.WriteFile(Path(), []byte(`{"mode":"portable","skill_dir":"cwd-skills","tools":{"node":"cwd-node-reloaded"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if node := Load().Tools["node"]; node != "cwd-node-reloaded" {
				t.Fatalf("selected file was not reloaded: node=%q", node)
			}
			exeConfig := filepath.Join(BaseDir(), "config.json")
			t.Setenv("ARTEX_CONFIG", exeConfig)
			if Path() != exeConfig || Load().Tools["node"] != "exe-node" {
				t.Fatal("explicit config did not override selected file")
			}
			t.Setenv("ARTEX_CONFIG", "")
			for _, source := range []string{"cli", "env"} {
				SetRuntimeMode("")
				t.Setenv("VULNHARVEST_MODE", "")
				mode, err := ResolveMode("")
				if err != nil {
					t.Fatal(err)
				}
				SetRuntimeMode(mode)
				cli := "portable"
				if source == "env" {
					cli = ""
					t.Setenv("VULNHARVEST_MODE", "portable")
				}
				mode, err = ResolveMode(cli)
				if err != nil || mode != "portable" {
					t.Fatalf("%s mode=%q err=%v", source, mode, err)
				}
				SetRuntimeMode(mode)
				if Path() != exeConfig || Load().Tools["node"] != "exe-node" || SkillPath() != filepath.Join(BaseDir(), "exe-skills") {
					t.Fatalf("%s inherited old file selection: path=%q skills=%q", source, Path(), SkillPath())
				}
			}
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"postgres-both", "postgres-cwd-only", "explicit-config", "portable-env-relative", "portable-file-relative", "portable-absolute", "postgres-relative", "inferred-file-mode", "postgres-both-ancestor-symlink", "inferred-file-mode-ancestor-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			aliasCWD := strings.HasSuffix(scenario, "-ancestor-symlink")
			scenario = strings.TrimSuffix(scenario, "-ancestor-symlink")
			base, cwd := t.TempDir(), configProcessCWD(t, aliasCWD)
			child := filepath.Join(base, "config-fixture.exe")
			if err := os.WriteFile(child, b, 0700); err != nil {
				t.Fatal(err)
			}
			baseConfig, cwdConfig := filepath.Join(base, "config.json"), filepath.Join(cwd, "config.json")
			for _, path := range []string{baseConfig, cwdConfig} {
				if err := os.WriteFile(path, []byte(`{"skill_dir":"user-skills"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mode, override, skillOverride := "postgres", "", ""
			infer := ""
			wantConfig, wantSkills := cwdConfig, "user-skills"
			switch scenario {
			case "postgres-cwd-only":
				if err := os.Remove(baseConfig); err != nil {
					t.Fatal(err)
				}
			case "explicit-config":
				override, wantConfig = baseConfig, baseConfig
			case "portable-env-relative", "portable-file-relative", "portable-absolute":
				mode, wantConfig, wantSkills = "portable", baseConfig, filepath.Join(base, "user-skills")
				if scenario == "portable-env-relative" {
					skillOverride = "user-skills"
				}
				if scenario == "portable-absolute" {
					skillOverride, wantSkills = t.TempDir(), ""
					wantSkills = skillOverride
				}
			case "inferred-file-mode":
				mode, infer, wantSkills = "", "1", filepath.Join(base, "cwd-skills")
				if err := os.WriteFile(baseConfig, []byte(`{"mode":"postgres","skill_dir":"exe-skills","tools":{"node":"exe-node"}}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cwdConfig, []byte(`{"mode":"portable","skill_dir":"cwd-skills","tools":{"node":"cwd-node"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				cmd := exec.Command(child, "-test.run=^TestConfigPathActualProcess$")
				cmd.Dir = cwd
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if key != "ARTEX_CONFIG" && key != "ARTEX_SKILL_DIR" && key != "VULNHARVEST_MODE" {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "ARTEX_PATH_CHILD=1", "ARTEX_PATH_MODE="+mode, "ARTEX_PATH_INFER="+infer, "ARTEX_PATH_WANT_CONFIG="+wantConfig, "ARTEX_PATH_WANT_SKILLS="+wantSkills, "ARTEX_CONFIG="+override, "ARTEX_SKILL_DIR="+skillOverride)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("cwd %d: %v: %s", i, err, out)
				}
				if aliasCWD && scenario == "postgres-both" {
					// Both files exist: the exact oracle must still reject an executable-file expectation.
					wrong := exec.Command(child, "-test.run=^TestConfigPathActualProcess$")
					wrong.Dir, wrong.Env = cmd.Dir, append([]string(nil), cmd.Env...)
					for j, entry := range wrong.Env {
						if strings.HasPrefix(entry, "ARTEX_PATH_WANT_CONFIG=") {
							wrong.Env[j] = "ARTEX_PATH_WANT_CONFIG=" + baseConfig
						}
					}
					out, err := wrong.CombinedOutput()
					if err == nil || !strings.Contains(string(out), fmt.Sprintf("Path=%q want %q", wantConfig, baseConfig)) {
						t.Fatalf("cwd %d: executable-file oracle was not rejected: err=%v output=%s", i, err, out)
					}
					t.Logf("cwd %d: exact oracle rejected executable config %q", i, baseConfig)
				}
				cwd = configProcessCWD(t, aliasCWD)
				if (mode == "postgres" || infer == "1") && override == "" {
					wantConfig = filepath.Join(cwd, "config.json")
				}
				body := []byte(`{"skill_dir":"user-skills"}`)
				if infer == "1" {
					body = []byte(`{"mode":"portable","skill_dir":"cwd-skills","tools":{"node":"cwd-node"}}`)
				}
				if err := os.WriteFile(filepath.Join(cwd, "config.json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Alias an ancestor, not the CWD itself, to reproduce macOS /var -> /private/var.
func configProcessCWD(t *testing.T, alias bool) string {
	t.Helper()
	cwd := t.TempDir()
	if alias {
		if runtime.GOOS == "windows" {
			t.Skip("ancestor symlink fixture requires Unix symlink semantics")
		}
		physicalParent, aliasParent := filepath.Join(cwd, "physical"), filepath.Join(cwd, "alias")
		if err := os.MkdirAll(filepath.Join(physicalParent, "cwd"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(physicalParent, aliasParent); err != nil {
			t.Fatal(err)
		}
		cwd = filepath.Join(aliasParent, "cwd")
	}
	// os.Getwd in the child can return a physical path even when TempDir uses an
	// alias (macOS /var). Resolve the existing CWD once for cmd.Dir and its oracle;
	// leave executable and explicit override paths under their original contracts.
	physical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if alias {
		if physical == cwd {
			t.Fatalf("ancestor symlink did not change CWD spelling: %q", cwd)
		}
		t.Logf("ancestor alias CWD=%q physical CWD=%q", cwd, physical)
	}
	return physical
}
