package authoring

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"aster/contracts/volumemanifest"
)

// Workbench queries use the current cursor and ordering contracts.
type Query struct {
	Profile   string `json:"profile"`
	Q         string `json:"q"`
	Status    string `json:"status"`
	Changes   string `json:"changes"`
	Sort      string `json:"sort"`
	Direction string `json:"direction"`
	Parent    string `json:"parent"`
	Cursor    string `json:"-"`
	Limit     int    `json:"limit"`
	Revision  int64  `json:"revision"`
}
type Page[T any] struct {
	Items    []T    `json:"items"`
	Next     string `json:"next"`
	Total    int    `json:"total"`
	Revision int64  `json:"revision"`
}
type LibrarySummary struct {
	Library
	UpdatedAt              int64           `json:"updated_at"`
	DraftChangeCount       int             `json:"draft_change_count"`
	LatestPublishedVersion *VersionSummary `json:"latest_published_version"`
}
type VersionSummary struct {
	Version
	CreatedAt   int64  `json:"created_at"`
	PublishedAt *int64 `json:"published_at"`
}
type workbenchCursor struct {
	Scope    string `json:"scope"`
	Query    string `json:"query"`
	Offset   int    `json:"offset"`
	Snapshot string `json:"snapshot"`
}

func pageOf[T any](items []T, q Query, scope string, defaultLimit int) (Page[T], error) {
	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
	if q.Limit < 1 || q.Limit > 200 {
		return Page[T]{}, ErrInvalid
	}
	raw, _ := json.Marshal(q)
	fingerprint := digestBytes(raw)
	snapshotRaw, _ := json.Marshal(items)
	snapshot := digestBytes(snapshotRaw)
	start := 0
	if q.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var c workbenchCursor
		if err != nil || json.Unmarshal(data, &c) != nil || c.Scope != scope || c.Query != fingerprint || c.Offset < 0 {
			return Page[T]{}, ErrInvalid
		}
		if c.Snapshot != snapshot {
			return Page[T]{}, ErrConflict
		}
		start = c.Offset
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + q.Limit
	if end > len(items) {
		end = len(items)
	}
	p := Page[T]{Items: append([]T{}, items[start:end]...), Total: len(items), Revision: q.Revision}
	if end < len(items) {
		data, _ := json.Marshal(workbenchCursor{scope, fingerprint, end, snapshot})
		p.Next = base64.RawURLEncoding.EncodeToString(data)
	}
	return p, nil
}
func (s *Store) summaries(ctx context.Context, library string) ([]LibrarySummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+libraryColumns+`,max(created_at,coalesce((SELECT max(a.created_at) FROM audit a WHERE a.library_id=libraries.id),0)) FROM libraries WHERE (?='' OR id=?)`, library, library)
	if err != nil {
		return nil, err
	}
	out := []LibrarySummary{}
	for rows.Next() {
		var l LibrarySummary
		if err = rows.Scan(&l.ID, &l.Name, &l.Description, &l.Profile, &l.MetadataRevision, &l.Archived, &l.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		l.Builtin = l.ID == "aster-shared-assets"
		out = append(out, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Draft changes belong to working branches and compare against each branch's
	// committed head, never the last published version of the library.
	counts := map[string]int{}
	rows, err = s.db.QueryContext(ctx, "SELECT id,library_id FROM branches WHERE state='ready' AND (?='' OR library_id=?)", library, library)
	if err != nil {
		return nil, err
	}
	type branchRef struct{ id, library string }
	branches := []branchRef{}
	for rows.Next() {
		var b branchRef
		if err = rows.Scan(&b.id, &b.library); err != nil {
			rows.Close()
			return nil, err
		}
		branches = append(branches, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, b := range branches {
		draft, err := entries(ctx, s.db, "branch_entries", "branch_id", b.id)
		if err != nil {
			return nil, err
		}
		repo, err := s.historyRepository(ctx, b.library)
		if err != nil {
			return nil, err
		}
		head, err := repo.Head(ctx, "refs/heads/"+b.id)
		if err != nil {
			return nil, err
		}
		snapshot, err := repo.Snapshot(ctx, head)
		if err != nil {
			return nil, err
		}
		counts[b.library] += len(compareEntries(snapshot.Entries, draft))
	}
	versions, err := s.versionSummaries(ctx, library)
	if err != nil {
		return nil, err
	}
	latest := map[string]*VersionSummary{}
	for i := range versions {
		v := &versions[i]
		if v.State == "published" {
			old := latest[v.LibraryID]
			if old == nil || versionTime(*v) > versionTime(*old) || versionTime(*v) == versionTime(*old) && v.ID > old.ID {
				latest[v.LibraryID] = v
			}
		}
	}
	for i := range out {
		l := &out[i]
		l.DraftChangeCount = counts[l.ID]
		l.LatestPublishedVersion = latest[l.ID]
	}
	return out, nil
}
func versionTime(v VersionSummary) int64 {
	if v.PublishedAt != nil {
		return *v.PublishedAt
	}
	return v.CreatedAt
}
func (s *Store) WorkbenchCatalog(ctx context.Context, q Query) (Page[LibrarySummary], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.Profile != "" && !manifest.SupportedProfile(q.Profile) || q.Status != "" && q.Status != "active" && q.Status != "archived" && q.Status != "all" || q.Changes != "" && q.Changes != "yes" && q.Changes != "no" || q.Sort != "" && q.Sort != "name" && q.Sort != "status" && q.Sort != "updated" || q.Direction != "" && q.Direction != "asc" && q.Direction != "desc" {
		return Page[LibrarySummary]{}, ErrInvalid
	}
	all, err := s.summaries(ctx, "")
	if err != nil {
		return Page[LibrarySummary]{}, err
	}
	items := []LibrarySummary{}
	needle := strings.ToLower(q.Q)
	for _, l := range all {
		if q.Profile != "" && q.Profile != l.Profile {
			continue
		}
		if !strings.Contains(strings.ToLower(l.Name+"\n"+l.Description), needle) || q.Status == "active" && l.Archived || q.Status == "archived" && !l.Archived || q.Changes == "yes" && l.DraftChangeCount == 0 || q.Changes == "no" && l.DraftChangeCount != 0 {
			continue
		}
		items = append(items, l)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		cmp := 0
		switch q.Sort {
		case "name":
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case "status":
			if a.Archived != b.Archived {
				if a.Archived {
					cmp = 1
				} else {
					cmp = -1
				}
			}
		default:
			if a.UpdatedAt < b.UpdatedAt {
				cmp = -1
			} else if a.UpdatedAt > b.UpdatedAt {
				cmp = 1
			}
		}
		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}
		if q.Direction == "asc" {
			return cmp < 0
		}
		return cmp > 0
	})
	return pageOf(items, q, "libraries", 50)
}
func (s *Store) Summary(ctx context.Context, id string) (LibrarySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.summaries(ctx, id)
	if err != nil {
		return LibrarySummary{}, err
	}
	for _, l := range all {
		if l.ID == id {
			return l, nil
		}
	}
	return LibrarySummary{}, ErrNotFound
}
func (s *Store) versionSummaries(ctx context.Context, library string) ([]VersionSummary, error) {
	query := `SELECT ` + versionColumns + `,created_at,published_at FROM versions`
	args := []any{}
	if library != "" {
		query += " WHERE library_id=?"
		args = append(args, library)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionSummary{}
	for rows.Next() {
		var v VersionSummary
		if err = rows.Scan(&v.ID, &v.LibraryID, &v.Label, &v.Note, &v.DescriptorDigest, &v.State, &v.Publisher.Issuer, &v.Publisher.Subject, &v.Error, &v.Generation, &v.PublisherID, &v.Profile, &v.AcceptedCommit, &v.CreatedAt, &v.PublishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) VersionCatalog(ctx context.Context, id string, q Query) (Page[VersionSummary], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, id); err != nil {
		return Page[VersionSummary]{}, err
	}
	all, err := s.versionSummaries(ctx, id)
	if err != nil {
		return Page[VersionSummary]{}, err
	}
	out := []VersionSummary{}
	for _, v := range all {
		if strings.Contains(strings.ToLower(v.Label), strings.ToLower(q.Q)) && (q.Status == "" || q.Status == "all" || q.Status == v.State || q.Status == "readable" && (v.State == "published" || v.State == "archived")) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return pageOf(out, q, "versions/"+id, 50)
}
func (s *Store) WorkbenchTree(ctx context.Context, id, version string, q Query, search bool) (Page[manifest.Entry], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.Parent != "" && manifest.ValidPath(q.Parent, true) != nil {
		return Page[manifest.Entry]{}, ErrInvalid
	}
	if !manifest.ValidID(version) {
		return Page[manifest.Entry]{}, ErrInvalid
	}
	v, err := s.Version(ctx, version)
	if err != nil {
		return Page[manifest.Entry]{}, err
	}
	if v.LibraryID != id || (v.State != "published" && v.State != "archived") {
		return Page[manifest.Entry]{}, ErrNotFound
	}
	all, err := entries(ctx, s.db, "version_entries", "version_id", version)
	if err != nil {
		return Page[manifest.Entry]{}, err
	}
	out := []manifest.Entry{}
	for _, e := range all {
		if search {
			if strings.Contains(strings.ToLower(e.Path), strings.ToLower(q.Q)) {
				out = append(out, e)
			}
		} else if parentOf(e.Path) == q.Parent {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !search && a.Kind != b.Kind {
			return a.Kind == "directory"
		}
		return a.Path < b.Path
	})
	mode := "tree"
	if search {
		mode = "search"
	}
	return pageOf(out, q, id+"/"+version+"/"+mode, 200)
}
