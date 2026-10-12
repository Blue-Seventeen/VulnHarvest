package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRedactSecrets(t *testing.T) {
	for _, input := range []string{
		"postgres://u:S3CRET@host/db?password=Q3SECRET&sslmode=disable",
		"host=host user=u password='KEYSECRET with space' dbname=db",
		"postgres://u:BROKENSECRET%zz@host/db?password=BADSECRET",
		"http://u:PROXYSECRET@host:8/?token=TOKENSECRET",
	} {
		got := Redact(input)
		for _, secret := range []string{"S3CRET", "Q3SECRET", "KEYSECRET", "BROKENSECRET", "BADSECRET", "PROXYSECRET", "TOKENSECRET"} {
			if strings.Contains(got, secret) {
				t.Errorf("secret %s in %q", secret, got)
			}
		}
	}
}

func TestRuntimePortableBaseActualExecutable(t *testing.T) {
	if os.Getenv("VH_CONFIG_CHILD") == "1" {
		if BaseDir() != filepath.Dir(os.Args[0]) {
			t.Fatalf("base=%q exe=%q", BaseDir(), os.Args[0])
		}
		if Path() != filepath.Join(filepath.Dir(os.Args[0]), "config.json") {
			t.Fatalf("config=%q", Path())
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	child := filepath.Join(dir, "fixture.exe")
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(child, b, 0700); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "config.json"), []byte(`{"mode":"postgres"}`), 0600)
	cmd := exec.Command(child, "-test.run=^TestRuntimePortableBaseActualExecutable$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "VH_CONFIG_CHILD=1", "VULNHARVEST_MODE=portable", "ARTEX_CONFIG=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
