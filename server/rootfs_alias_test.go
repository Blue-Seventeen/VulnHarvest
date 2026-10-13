package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/internal/testenv"
)

// A real ancestor alias must not turn owned CRUD into a lexical escape. These
// tests also reject outside aliases pointing inward and inside links pointing out.
func TestRootFSAliasCRUDAndBoundary(t *testing.T) {
	for _, aliased := range []bool{false, true} {
		name := "ordinary"
		if aliased {
			name = "ancestor-alias"
		}
		t.Run(name, func(t *testing.T) {
			physical, err := filepath.EvalSymlinks(testenv.LinkTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			logical := physical
			if aliased {
				logical = rootFSAlias(t, physical)
			}
			base := filepath.Join(logical, "managed")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			f := rootFS{base: base}
			dir := filepath.Join(base, "new", "nested")
			if err := f.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "receipt.txt")
			if err := f.WriteFile(path, []byte("owned-receipt"), 0600); err != nil {
				t.Fatal(err)
			}
			// Both spellings identify the same managed root.
			for _, readPath := range []string{path, filepath.Join(physical, "managed", "new", "nested", "receipt.txt")} {
				if raw, err := f.ReadFile(readPath); err != nil || string(raw) != "owned-receipt" {
					t.Fatalf("read owned receipt: %q %v", raw, err)
				}
			}
			if err := f.Rename(path, filepath.Join(dir, "renamed.txt")); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(physical, "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(state, "keep.txt")
			if err := os.WriteFile(secret, []byte("state-receipt"), 0600); err != nil {
				t.Fatal(err)
			}
			runtimeLink(t, state, filepath.Join(base, "escape"))
			inward := filepath.Join(physical, "inward")
			runtimeLink(t, filepath.Join(physical, "managed"), inward)
			for _, outside := range []string{secret, filepath.Join(base, "..", "state", "keep.txt"), filepath.Join(base, "escape", "keep.txt"), filepath.Join(inward, "new", "nested", "renamed.txt")} {
				if _, err := f.ReadFile(outside); err == nil {
					t.Fatalf("outside read allowed: %s", outside)
				}
				if err := f.WriteFile(outside, []byte("overwrite"), 0600); err == nil {
					t.Fatalf("outside write allowed: %s", outside)
				}
			}
			if err := f.MkdirAll(filepath.Join(base, "escape", "missing", "nested"), 0700); err == nil {
				t.Fatal("missing suffix beneath escaping link accepted")
			}
			if _, err := os.Stat(filepath.Join(state, "missing")); !os.IsNotExist(err) {
				t.Fatalf("outside missing suffix created: %v", err)
			}
			if raw, err := os.ReadFile(secret); err != nil || string(raw) != "state-receipt" {
				t.Fatalf("state changed: %q %v", raw, err)
			}
			if raw, err := f.ReadFile(filepath.Join(dir, "renamed.txt")); err != nil || string(raw) != "owned-receipt" {
				t.Fatalf("inward alias mutated owned file: %q %v", raw, err)
			}
			if err := f.RemoveAll(base); err == nil {
				t.Fatal("managed root removed")
			}
			if err := f.RemoveAll(filepath.Join(base, "new")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(base, "new")); !os.IsNotExist(err) {
				t.Fatalf("owned delete failed: %v", err)
			}
		})
	}
}

func TestRootFSAliasAuthenticatedAPI(t *testing.T) {
	physical, err := filepath.EvalSymlinks(testenv.LinkTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	logical := rootFSAlias(t, physical)
	h := newCapabilityHarnessAt(t, logical)
	api := controlledAPI(t, h.s)
	api("POST", "/api/skills", map[string]any{"name": "alias-skill", "description": "alias", "instructions": "owned skill"}, 201)
	api("PUT", "/api/skills/alias-skill/files/references/receipt.md", map[string]any{"content": "skill-receipt"}, 200)
	if got := api("GET", "/api/skills/alias-skill/files/references/receipt.md", nil, 200)["content"]; got != "skill-receipt" {
		t.Fatalf("skill content: %v", got)
	}
	api("POST", "/api/workspace/write", map[string]any{"path": "nested/receipt.txt", "content": "workspace-receipt"}, 200)
	if got := api("GET", "/api/workspace/read?path=nested/receipt.txt", nil, 200)["content"]; got != "workspace-receipt" {
		t.Fatalf("workspace content: %v", got)
	}
	secret := filepath.Join(h.state, "alias-state.txt")
	if err := os.WriteFile(secret, []byte("state-receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeLink(t, h.state, filepath.Join(h.data, "escape"))
	runtimeLink(t, h.state, filepath.Join(h.skills, "alias-skill", "escape"))
	api("GET", "/api/workspace/read?path=escape/alias-state.txt", nil, 404)
	api("POST", "/api/workspace/write", map[string]any{"path": "escape/new/receipt.txt", "content": "overwrite"}, 500)
	api("GET", "/api/skills/alias-skill/files/escape/alias-state.txt", nil, 500)
	api("PUT", "/api/skills/alias-skill/files/escape/new/receipt.txt", map[string]any{"content": "overwrite"}, 500)
	if raw, err := os.ReadFile(secret); err != nil || string(raw) != "state-receipt" {
		t.Fatalf("state changed: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(h.state, "new")); !os.IsNotExist(err) {
		t.Fatalf("state suffix created: %v", err)
	}
	// Remove the test links before API deletion so cleanup never sees stale links.
	for _, link := range []string{filepath.Join(h.data, "escape"), filepath.Join(h.skills, "alias-skill", "escape")} {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	api("DELETE", "/api/skills/alias-skill/files/references/receipt.md", nil, 200)
	api("GET", "/api/skills/alias-skill/files/references/receipt.md", nil, 404)
	api("DELETE", "/api/skills/alias-skill", nil, 200)
	api("DELETE", "/api/workspace/delete?path=nested", nil, 200)
	api("GET", "/api/workspace/read?path=nested/receipt.txt", nil, 404)
}
