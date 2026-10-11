package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModePrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("ARTEX_CONFIG", path)
	t.Setenv("ARTEX_PG_DSN", "postgres://invalid-host/must-not-connect")
	for _, tc := range []struct {
		name, cli, env, file, want string
		invalid                    bool
	}{
		{name: "default", want: "postgres"},
		{name: "file", file: "portable", want: "portable"},
		{name: "env", env: "postgres", file: "portable", want: "postgres"},
		{name: "cli", cli: "portable", env: "postgres", file: "postgres", want: "portable"},
		{name: "normalize", cli: " Portable ", want: "portable"},
		{name: "invalid-cli", cli: "sqlite", invalid: true},
		{name: "invalid-env", env: "auto", invalid: true},
		{name: "invalid-file", file: "unknown", invalid: true},
		{name: "override-invalid", cli: "portable", env: "invalid", file: "invalid", want: "portable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VULNHARVEST_MODE", tc.env)
			if err := os.WriteFile(path, []byte(`{"mode":"`+tc.file+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ResolveMode(tc.cli)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid mode accepted")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
