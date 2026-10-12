package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var ownedArchiveName = regexp.MustCompile(`^(exp[1-9][0-9]*-[A-Za-z0-9_.-]+|conv-[1-9][0-9]*(\.jsonl)?|chat-conv-[1-9][0-9]*)$`)
var conversationSideArtifact = regexp.MustCompile(`^exp0-btw-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(\.jsonl)?$`)

type archiveArtifactOwner struct {
	ExplorationID   int64    `json:"exploration_id,omitempty"`
	ConversationIDs []int64  `json:"conversation_ids,omitempty"`
	SideRequestIDs  []string `json:"side_request_ids,omitempty"`
}

func ownerFromSnapshot(snapshot *db.TaskArchiveSnapshot) (archiveArtifactOwner, error) {
	if snapshot == nil || snapshot.TaskID <= 0 || snapshot.ExplorationID <= 0 {
		return archiveArtifactOwner{}, errors.New("archive snapshot identity missing")
	}
	owner := archiveArtifactOwner{ExplorationID: snapshot.ExplorationID}
	var conversations []struct {
		ID int64 `json:"id"`
	}
	if raw := snapshot.Tables["conversations"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &conversations); err != nil {
			return owner, err
		}
	}
	allowed := map[string]bool{}
	for _, c := range conversations {
		if c.ID <= 0 {
			return owner, errors.New("invalid snapshot conversation identity")
		}
		owner.ConversationIDs = append(owner.ConversationIDs, c.ID)
		allowed["conv-"+strconv.FormatInt(c.ID, 10)] = true
	}
	var sides []struct {
		ID         string `json:"id"`
		SessionKey string `json:"session_key"`
	}
	if raw := snapshot.Tables["side_question_requests"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &sides); err != nil {
			return owner, err
		}
	}
	for _, side := range sides {
		if allowed[side.SessionKey] {
			if !conversationSideArtifact.MatchString("exp0-btw-" + side.ID) {
				return owner, errors.New("invalid snapshot side request identity")
			}
			owner.SideRequestIDs = append(owner.SideRequestIDs, side.ID)
		}
	}
	return owner, nil
}
func (owner archiveArtifactOwner) owns(relative, taskID string) bool {
	if !validPortableOwnedPath(relative) {
		return false
	}
	parts := strings.Split(relative, "/")
	name := parts[1]
	if parts[0] == "tasks" {
		return name == taskID
	}
	if match := conversationSideArtifact.FindStringSubmatch(name); match != nil {
		if parts[0] != "transcripts" {
			return false
		}
		for _, id := range owner.SideRequestIDs {
			if match[1] == id {
				return true
			}
		}
		return false
	}
	if owner.ExplorationID > 0 && strings.HasPrefix(name, fmt.Sprintf("exp%d-", owner.ExplorationID)) {
		return true
	}
	if parts[0] == "noa" {
		name = strings.TrimPrefix(name, "chat-")
	}
	name = strings.TrimSuffix(name, ".jsonl")
	for _, id := range owner.ConversationIDs {
		if name == "conv-"+strconv.FormatInt(id, 10) {
			return true
		}
	}
	return false
}

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
	return (ownedArchiveName.MatchString(parts[1]) || parts[0] == "transcripts" && conversationSideArtifact.MatchString(parts[1])) && !strings.Contains(parts[1], "..")
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

// Legacy absolute addresses are valid only at the current root, never rebased.
// Every endpoint is reduced to the same exact relative namespace before use.
func journalRelative(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		path = filepath.ToSlash(rel)
	}
	if !validPortableOwnedPath(path) {
		return "", errors.New("invalid portable journal namespace")
	}
	return path, nil
}
func resolvePortableStageJournal(data string, journal *archiveStageJournal) error {
	taskID, ok := canonicalTaskID(journal.TaskID)
	if journal.ArchiveID <= 0 || !ok || taskID != journal.TaskID {
		return errors.New("invalid portable staging identity")
	}
	for i := range journal.Moves {
		move := &journal.Moves[i]
		relative, err := journalRelative(data, move.Source)
		if err != nil {
			return err
		}
		if filepath.ToSlash(move.Relative) != "files/"+relative {
			return errors.New("invalid portable staging endpoint relationship")
		}
		if strings.HasPrefix(relative, "tasks/") && relative != "tasks/"+taskID {
			return errors.New("staging journal task mismatch")
		}
		if !journal.archiveArtifactOwner.owns(relative, taskID) {
			return errors.New("staging journal ownership mismatch")
		}
		path := filepath.Join(data, filepath.FromSlash(relative))
		if err := validateArchiveEndpoint(data, path); err != nil {
			return err
		}
		move.Source = path
	}
	return nil
}
func resolvePortableRestoreJournal(data, extracted string, journal *archiveRestoreJournal) error {
	if journal.ArchiveID <= 0 || journal.TaskID <= 0 {
		return errors.New("invalid portable restore identity")
	}
	prefix := strconv.FormatInt(journal.ArchiveID, 10) + "-"
	if filepath.Dir(extracted) != filepath.Join(taskArchiveRoot(data), ".restore") || !strings.HasPrefix(filepath.Base(extracted), prefix) {
		return errors.New("invalid portable restore staging identity")
	}
	for i := range journal.Moves {
		move := &journal.Moves[i]
		relative, err := journalRelative(data, move.Destination)
		if err != nil {
			return err
		}
		source := move.Source
		if filepath.IsAbs(source) {
			rel, err := filepath.Rel(extracted, source)
			if err != nil {
				return err
			}
			source = filepath.ToSlash(rel)
		}
		if source != "files/"+relative {
			return errors.New("invalid portable restore endpoint relationship")
		}
		taskID := strconv.FormatInt(journal.TaskID, 10)
		if strings.HasPrefix(relative, "tasks/") && relative != "tasks/"+taskID {
			return errors.New("restore journal task mismatch")
		}
		if !journal.archiveArtifactOwner.owns(relative, taskID) {
			return errors.New("restore journal ownership mismatch")
		}
		move.Source = filepath.Join(extracted, filepath.FromSlash(source))
		move.Destination = filepath.Join(data, filepath.FromSlash(relative))
		for _, path := range []string{move.Source, move.Destination} {
			if err := validateArchiveEndpoint(data, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func liveArchiveOwner(pg *db.DB, taskID int64) (archiveArtifactOwner, error) {
	var owner archiveArtifactOwner
	if err := pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&owner.ExplorationID); err != nil {
		return owner, err
	}
	var err error
	owner.ConversationIDs, err = pg.TaskArchiveConversationIDs(taskID)
	if err != nil {
		return owner, err
	}
	owner.SideRequestIDs, err = pg.TaskArchiveConversationSideRequestIDs(taskID)
	return owner, err
}
func validateArchiveEndpoint(data, path string) error {
	root, err := os.OpenRoot(data)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(data, path)
	if err != nil {
		return err
	}
	// Stat resolves links; a missing leaf is valid, but its existing ancestors
	// are still walked by Root and an escaping link is a different error.
	_, err = root.Stat(rel)
	if os.IsNotExist(err) {
		return nil
	}
	return err
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
