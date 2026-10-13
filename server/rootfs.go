package server

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// rootFS confines the actual syscall, including symlink/junction resolution.
// Absolute paths are accepted only to retain existing handler path rendering.
type rootFS struct{ base string }

func (f rootFS) root(path string) (*os.Root, string, error) {
	base, err := filepath.Abs(f.base)
	if err != nil {
		return nil, "", err
	}
	physicalBase, err := realRootPath(base)
	if err != nil {
		return nil, "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	// Compare like spellings: handlers retain the configured ancestor alias,
	// while other internal callers may already use its physical path. Do not
	// admit arbitrary outside aliases merely because they point into this root.
	if !pathsContain(base, abs) && !pathsContain(physicalBase, abs) {
		return nil, "", fmt.Errorf("path escapes managed root")
	}
	// os.Root rejects absolute link targets even when inside the root. Resolve a
	// valid existing ancestor, then open its canonical relative path with Root:
	// changing the original link afterwards cannot redirect this syscall.
	resolved, err := canonicalFuturePath(abs)
	if err != nil {
		return nil, "", err
	}
	if !pathsContain(physicalBase, resolved) {
		return nil, "", fmt.Errorf("link escapes managed root")
	}
	rel, err := filepath.Rel(physicalBase, resolved)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(physicalBase)
	return root, rel, err
}
func (f rootFS) Open(path string) (*os.File, error) {
	r, p, e := f.root(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.Open(p)
}
func (f rootFS) Create(path string) (*os.File, error) {
	r, p, e := f.root(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.Create(p)
}
func (f rootFS) Stat(path string) (os.FileInfo, error) {
	r, p, e := f.root(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.Stat(p)
}
func (f rootFS) ReadFile(path string) ([]byte, error) {
	r, p, e := f.root(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.ReadFile(p)
}
func (f rootFS) WriteFile(path string, b []byte, mode os.FileMode) error {
	r, p, e := f.root(path)
	if e != nil {
		return e
	}
	defer r.Close()
	return r.WriteFile(p, b, mode)
}
func (f rootFS) MkdirAll(path string, mode os.FileMode) error {
	r, p, e := f.root(path)
	if e != nil {
		return e
	}
	defer r.Close()
	return r.MkdirAll(p, mode)
}
func (f rootFS) RemoveAll(path string) error {
	r, p, e := f.root(path)
	if e != nil {
		return e
	}
	defer r.Close()
	if p == "." {
		return fmt.Errorf("cannot remove managed root")
	}
	return r.RemoveAll(p)
}
func (f rootFS) Rename(old, new string) error {
	r, p, e := f.root(old)
	if e != nil {
		return e
	}
	defer r.Close()
	r2, p2, e := f.root(new)
	if e != nil {
		return e
	}
	r2.Close()
	return r.Rename(p, p2)
}
func (f rootFS) ReadDir(path string) ([]os.DirEntry, error) {
	file, e := f.Open(path)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	return file.ReadDir(-1)
}
func (f rootFS) Walk(path string) ([]string, error) {
	r, p, e := f.root(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	var out []string
	// OpenRoot validates the skill directory itself before WalkDir follows it.
	child, e := r.OpenRoot(p)
	if e != nil {
		return nil, e
	}
	defer child.Close()
	e = fs.WalkDir(child.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if d.IsDir() {
			path += "/"
		}
		out = append(out, path)
		return nil
	})
	return out, e
}
func (s *Server) workspaceFS() rootFS { return rootFS{base: s.m.dir} }
func (s *Server) skillFS() rootFS     { return rootFS{base: s.skillDir} }
