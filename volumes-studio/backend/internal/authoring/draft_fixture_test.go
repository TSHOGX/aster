package authoring

// Old snapshot fixtures use library drafts only in test binaries. Runtime edits
// use branch commands and cannot mutate a library-wide draft.
import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

type fixtureMutation struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Path             string `json:"path"`
	To               string `json:"to,omitempty"`
	Text             string `json:"text,omitempty"`
	Recursive        bool   `json:"recursive,omitempty"`
	VersionID        string `json:"version_id,omitempty"`
}

func validateDraft(entries []manifest.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	_, err := manifest.New("validation", "library", "draft", entries)
	return err
}
func mutateFixture(s *Store, ctx context.Context, a Actor, id string, input fixtureMutation) (MutationResult, error) {
	if !manifest.ValidID(id) || input.ExpectedRevision < 1 || !validText(input.Text, MaxTextBytes) {
		return MutationResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return MutationResult{}, ErrBusy
	}
	var result MutationResult
	body := requestDigest("mutate", id, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	l, err := s.Library(ctx, id)
	if err != nil {
		return result, err
	}
	if l.Archived || l.DraftRevision != input.ExpectedRevision {
		return result, ErrConflict
	}
	before, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return result, err
	}
	tree := map[string]manifest.Entry{}
	for _, e := range before {
		tree[e.Path] = e
	}
	var blob []byte
	install := false
	base := l.BaseVersion
	switch input.Action {
	case "mkdir":
		if input.To != "" || input.Text != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, true) != nil {
			return result, ErrInvalid
		}
		if _, ok := tree[input.Path]; ok {
			return result, ErrConflict
		}
		tree[input.Path] = manifest.Entry{Path: input.Path, Kind: "directory", Size: "0"}
		result.Affected = 1
	case "text":
		if input.To != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, false) != nil {
			return result, ErrInvalid
		}
		if existing, ok := tree[input.Path]; ok && existing.Kind != "file" {
			return result, ErrConflict
		}
		blob = []byte(input.Text)
		install = true
		tree[input.Path] = manifest.Entry{Path: input.Path, Kind: "file", Size: strconv.Itoa(len(blob)), Digest: digestBytes(blob)}
		result.Affected = 1
	case "remove":
		if input.To != "" || input.Text != "" || input.VersionID != "" || manifest.ValidPath(input.Path, false) != nil {
			return result, ErrInvalid
		}
		if _, ok := tree[input.Path]; !ok {
			return result, ErrNotFound
		}
		for name := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				result.Affected++
			}
		}
		if result.Affected > 1 && !input.Recursive {
			return result, ErrConflict
		}
		for name := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				delete(tree, name)
			}
		}
	case "move":
		e, ok := tree[input.Path]
		if !ok {
			return result, ErrNotFound
		}
		if input.Text != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, e.Kind == "directory") != nil || manifest.ValidPath(input.To, e.Kind == "directory") != nil || input.Path == input.To || strings.HasPrefix(input.To, input.Path+"/") {
			return result, ErrInvalid
		}
		if _, ok := tree[input.To]; ok {
			return result, ErrConflict
		}
		moved := []manifest.Entry{}
		for name, entry := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				entry.Path = input.To + strings.TrimPrefix(name, input.Path)
				if _, exists := tree[entry.Path]; exists {
					return result, ErrConflict
				}
				moved = append(moved, entry)
				delete(tree, name)
			}
		}
		for _, entry := range moved {
			tree[entry.Path] = entry
		}
		result.Affected = len(moved)
	case "restore":
		if input.Path != "" || input.To != "" || input.Text != "" || input.Recursive || !manifest.ValidID(input.VersionID) {
			return result, ErrInvalid
		}
		var state string
		if err = s.db.QueryRowContext(ctx, "SELECT state FROM versions WHERE id=? AND library_id=?", input.VersionID, id).Scan(&state); err != nil {
			return result, normalizeDB(err)
		}
		if state != "published" && state != "archived" {
			return result, ErrConflict
		}
		restored, err := entries(ctx, s.db, "version_entries", "version_id", input.VersionID)
		if err != nil {
			return result, err
		}
		tree = map[string]manifest.Entry{}
		for _, entry := range restored {
			tree[entry.Path] = entry
		}
		base = input.VersionID
		result.Affected = len(restored)
	default:
		return result, ErrInvalid
	}
	after := make([]manifest.Entry, 0, len(tree))
	for _, e := range tree {
		after = append(after, e)
	}
	if err = validateDraft(after); err != nil {
		return result, errors.Join(ErrInvalid, err)
	}
	// All conflict/structural checks precede disk work. GC cannot race installation
	// and attachment because both hold the same owner mutex and DB lifetime lock.
	if install {
		if _, err = s.install(ctx, blob); err != nil {
			return result, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = replaceEntries(ctx, tx, id, before, after); err != nil {
		return result, err
	}
	result.Revision = l.DraftRevision + 1
	if _, err = tx.ExecContext(ctx, "UPDATE libraries SET draft_revision=?,base_version=? WHERE id=?", result.Revision, base, id); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "draft."+input.Action, id, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func replaceEntries(ctx context.Context, tx *sql.Tx, id string, before, after []manifest.Entry) error {
	old := map[string]manifest.Entry{}
	next := map[string]manifest.Entry{}
	for _, e := range before {
		old[e.Path] = e
	}
	for _, e := range after {
		next[e.Path] = e
	}
	for _, e := range before {
		if _, ok := next[e.Path]; !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM draft_entries WHERE library_id=? AND path=?", id, e.Path); err != nil {
				return err
			}
		}
	}
	for _, e := range after {
		if previous, ok := old[e.Path]; ok && previous == e {
			continue
		}
		n, _ := manifest.ParseSize(e.Size)
		var digest any
		if e.Kind == "file" {
			digest = e.Digest
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO draft_entries VALUES(?,?,?,?,?) ON CONFLICT(library_id,path) DO UPDATE SET kind=excluded.kind,size=excluded.size,digest=excluded.digest`, id, e.Path, e.Kind, n, digest); err != nil {
			return err
		}
	}
	return nil
}
