package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Portable journal actions use Root for each actual syscall, not a checked
// absolute pathname followed by os.Rename/Open. Absolute internal symlinks are
// deliberately refused in recovery; ordinary workspace links use rootFS.
type archiveFileOps struct {
	base     string
	confined bool
}

func (f archiveFileOps) path(path string) (*os.Root, string, error) {
	root, err := os.OpenRoot(f.base)
	if err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(f.base, path)
	if err != nil {
		root.Close()
		return nil, "", err
	}
	return root, relative, nil
}
func (f archiveFileOps) Lstat(path string) (os.FileInfo, error) {
	if !f.confined {
		return os.Lstat(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	// Resolve parent/final links for admission, even though Lstat itself would
	// simply report a final link without following it.
	if _, e = r.Stat(p); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	return r.Lstat(p)
}
func (f archiveFileOps) Stat(path string) (os.FileInfo, error) {
	if !f.confined {
		return os.Stat(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.Stat(p)
}
func (f archiveFileOps) ReadFile(path string) ([]byte, error) {
	if !f.confined {
		return os.ReadFile(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return r.ReadFile(p)
}
func (f archiveFileOps) ReadDir(path string) ([]os.DirEntry, error) {
	if !f.confined {
		return os.ReadDir(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	file, e := r.Open(p)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	return file.ReadDir(-1)
}
func (f archiveFileOps) MkdirAll(path string, mode os.FileMode) error {
	if !f.confined {
		return os.MkdirAll(path, mode)
	}
	r, p, e := f.path(path)
	if e != nil {
		return e
	}
	defer r.Close()
	return r.MkdirAll(p, mode)
}
func (f archiveFileOps) RemoveAll(path string) error {
	if !f.confined {
		return os.RemoveAll(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return e
	}
	defer r.Close()
	if p == "." {
		return errors.New("cannot remove artifact root")
	}
	return r.RemoveAll(p)
}
func (f archiveFileOps) Remove(path string) error {
	if !f.confined {
		return os.Remove(path)
	}
	r, p, e := f.path(path)
	if e != nil {
		return e
	}
	defer r.Close()
	return r.Remove(p)
}
func (f archiveFileOps) Rename(old, new string) error {
	if !f.confined {
		return os.Rename(old, new)
	}
	r, p, e := f.path(old)
	if e != nil {
		return e
	}
	defer r.Close()
	other, e := filepath.Rel(f.base, new)
	if e != nil {
		return e
	}
	return r.Rename(p, other)
}
func (f archiveFileOps) writeJournal(path string, value any) error {
	if !f.confined {
		return writeArchiveJournal(path, value)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	root, p, err := f.path(path)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, archiveFileMode)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
