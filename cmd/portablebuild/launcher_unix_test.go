//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixLaunchersEmptyPATH(t *testing.T) {
	for _, tc := range []struct {
		name, source, expectedArgs string
	}{
		{"portable", "portable/start.sh", "arg:-mode\narg:portable\narg:中文 argument\n"},
		{"compatible", "start.sh", "arg:中文 argument\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			packageDir := filepath.Join(root, "中文 package")
			unrelated := filepath.Join(root, "unrelated cwd")
			for _, dir := range []string{packageDir, unrelated} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(tc.source)))
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(packageDir, "start.sh")
			if err := os.WriteFile(launcher, body, 0700); err != nil {
				t.Fatal(err)
			}
			// An actual executable fixture emits distinct stdout/stderr and exits 37.
			// Its shell uses only builtins; the launcher must need no PATH tools.
			if err := os.WriteFile(filepath.Join(packageDir, "artex"), []byte("#!/bin/sh\nprintf 'arg:%s\\n' \"$@\"\nprintf 'binary-stderr-marker\\n' >&2\nexit 37\n"), 0700); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(unrelated, launcher)
			if err != nil {
				t.Fatal(err)
			}
			for _, form := range []struct{ name, dir, argument string }{
				{"absolute", unrelated, launcher},
				{"relative", unrelated, relative},
				{"bare-name", packageDir, "start.sh"},
			} {
				t.Run(form.name, func(t *testing.T) {
					cmd := exec.Command("/bin/sh", form.argument, "中文 argument")
					cmd.Dir = form.dir
					cmd.Env = []string{"PATH="}
					var stdout, stderr strings.Builder
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					err := cmd.Run()
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 37 {
						t.Fatalf("actual binary exit not preserved: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
					if stdout.String() != tc.expectedArgs || stderr.String() != "binary-stderr-marker\n" {
						t.Fatalf("binary argv/stderr not preserved: stdout=%q stderr=%q", stdout.String(), stderr.String())
					}
				})
			}
		})
	}
}
