package server

import (
	"errors"
	"github.com/Autumn-27/artex/db"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var ownedArchiveName = regexp.MustCompile(`^(exp[1-9][0-9]*-[A-Za-z0-9_.-]+|conv-[1-9][0-9]*(\.jsonl)?|chat-conv-[1-9][0-9]*)$`)

func validPortableOwnedPath(relative string) bool {
	if strings.Contains(relative, "\\") || filepath.IsAbs(relative) || filepath.ToSlash(filepath.Clean(relative)) != relative {
		return false
	}
	parts := strings.Split(relative, "/")
	if len(parts) != 2 {
		return false
	}
	if parts[0] == "tasks" {
		n, e := strconv.ParseInt(parts[1], 10, 64)
		return e == nil && n > 0 && strconv.FormatInt(n, 10) == parts[1]
	}
	if parts[0] != "transcripts" && parts[0] != "sessions" && parts[0] != "noa" {
		return false
	}
	return ownedArchiveName.MatchString(parts[1]) && !strings.Contains(parts[1], "..")
}
func portableOwnedRelative(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	relative = filepath.ToSlash(relative)
	if !validPortableOwnedPath(relative) {
		return "", errors.New("invalid portable owned artifact namespace")
	}
	return relative, nil
}
func resolvePortableStageJournal(data string, journal *archiveStageJournal) error {
	if journal.ArchiveID <= 0 {
		return errors.New("invalid portable staging archive ID")
	}
	for i := range journal.Moves {
		move := &journal.Moves[i]
		if filepath.IsAbs(move.Source) {
			abs, err := filepath.Abs(move.Source)
			if err != nil {
				return err
			}
			root, err := filepath.Abs(data)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("absolute staging journal escapes managed root")
			}
			continue
		}
		if !validPortableOwnedPath(move.Source) || filepath.ToSlash(move.Relative) != "files/"+move.Source {
			return errors.New("invalid portable staging journal path")
		}
		if strings.HasPrefix(move.Source, "tasks/") && move.Source != "tasks/"+journal.TaskID {
			return errors.New("staging journal task mismatch")
		}
		path := filepath.Join(data, filepath.FromSlash(move.Source))
		r, _, err := (rootFS{base: data}).root(path)
		if err != nil {
			return err
		}
		r.Close()
		move.Source = path
	}
	return nil
}
func resolvePortableRestoreJournal(data, extracted string, journal *archiveRestoreJournal) error {
	if journal.ArchiveID <= 0 || journal.TaskID <= 0 {
		return errors.New("invalid portable restore identity")
	}
	for i := range journal.Moves {
		move := &journal.Moves[i]
		if filepath.IsAbs(move.Destination) {
			abs, err := filepath.Abs(move.Destination)
			if err != nil {
				return err
			}
			root, err := filepath.Abs(data)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("absolute restore journal escapes managed root")
			}
			continue
		}
		if !validPortableOwnedPath(move.Destination) || move.Source != "files/"+move.Destination {
			return errors.New("invalid portable restore journal path")
		}
		if strings.HasPrefix(move.Destination, "tasks/") && move.Destination != "tasks/"+strconv.FormatInt(journal.TaskID, 10) {
			return errors.New("restore journal task mismatch")
		}
		move.Source = filepath.Join(extracted, filepath.FromSlash(move.Source))
		move.Destination = filepath.Join(data, filepath.FromSlash(move.Destination))
		for _, p := range []string{move.Source, move.Destination} {
			r, _, err := (rootFS{base: data}).root(p)
			if err != nil {
				return err
			}
			r.Close()
		}
	}
	return nil
}
func (s *Server) persistedArchivePath(path string) string {
	if s.m.mode != "portable" {
		return path
	}
	relative, _ := filepath.Rel(s.m.dir, path)
	return filepath.ToSlash(relative)
}
func resolvePortableArchivePath(data string, job *db.TaskArchive) (string, error) {
	expected := taskArchivePath(data, job.ID, strconv.FormatInt(job.TaskID, 10))
	relative, _ := filepath.Rel(data, expected)
	if job.ID <= 0 || job.TaskID <= 0 || job.ArchivePath != filepath.ToSlash(relative) {
		return "", errors.New("invalid portable archive package reference")
	}
	r, _, err := (rootFS{base: data}).root(expected)
	if err != nil {
		return "", err
	}
	r.Close()
	return expected, nil
}
