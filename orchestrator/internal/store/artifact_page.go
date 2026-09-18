package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type ArtifactPageParams struct {
	Search                                      bool
	SessionID, LinkID, Directory, Query, Cursor string
	Limit                                       int
}
type ArtifactEntry struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	FileCount int64  `json:"file_count"`
	UpdatedAt string `json:"updated_at"`
}
type ArtifactPage struct {
	Entries    []ArtifactEntry `json:"entries"`
	NextCursor string          `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

func (s *Store) ArtifactPage(ctx context.Context, p ArtifactPageParams) (ArtifactPage, error) {
	out := ArtifactPage{Entries: []ArtifactEntry{}}
	if p.Limit <= 0 {
		p.Limit = 200
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	if strings.HasPrefix(p.Directory, "/") || strings.Contains(p.Directory, "\\") {
		return out, fmt.Errorf("invalid directory")
	}
	for _, part := range strings.Split(p.Directory, "/") {
		if part == ".." || part == "." {
			return out, fmt.Errorf("invalid directory")
		}
	}
	prefix := strings.TrimSuffix(p.Directory, "/")
	if prefix != "" {
		prefix += "/"
	}
	source := "SELECT path,size,updated_at FROM artifacts WHERE session_id=?"
	args := []any{p.SessionID}
	if p.LinkID != "" {
		source = "SELECT path,size,'' AS updated_at FROM share_link_files WHERE link_id=?"
		args = []any{p.LinkID}
	}
	// Range predicates keep directory lookup on the existing compound path index.
	source += " AND path>=? AND path<?"
	args = append(args, prefix, prefix+string(rune(0x10FFFF)))
	var grouped string
	if p.Search {
		source += " AND instr(lower(path),lower(?))>0"
		args = append(args, p.Query)
		grouped = "SELECT 'file' AS kind,path AS name,path,size,1 AS file_count,updated_at FROM source"
	} else {
		grouped = `SELECT CASE WHEN instr(substr(path,?),'/')>0 THEN 'directory' ELSE 'file' END AS kind,
 CASE WHEN instr(substr(path,?),'/')>0 THEN substr(substr(path,?),1,instr(substr(path,?),'/')-1) ELSE substr(path,?) END AS name,
 ? || CASE WHEN instr(substr(path,?),'/')>0 THEN substr(substr(path,?),1,instr(substr(path,?),'/')-1) ELSE substr(path,?) END AS path,
 sum(size) AS size,count(*) AS file_count,max(updated_at) AS updated_at FROM source GROUP BY kind,name`
		n := len([]rune(prefix)) + 1
		args = append(args, n, n, n, n, n, prefix, n, n, n, n)
	}
	where := ""
	if p.Cursor != "" {
		var cursor [2]string
		b, e := base64.RawURLEncoding.DecodeString(p.Cursor)
		if e != nil || json.Unmarshal(b, &cursor) != nil {
			return out, fmt.Errorf("invalid cursor")
		}
		where = " WHERE (kind,path)>(?,?)"
		args = append(args, cursor[0], cursor[1])
	}
	args = append(args, p.Limit+1)
	rows, err := s.reader().QueryContext(ctx, "WITH source AS ("+source+"), entries AS ("+grouped+") SELECT kind,name,path,size,file_count,updated_at FROM entries"+where+" ORDER BY kind,path LIMIT ?", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	size := 2
	for rows.Next() {
		var row ArtifactEntry
		if err = rows.Scan(&row.Kind, &row.Name, &row.Path, &row.Size, &row.FileCount, &row.UpdatedAt); err != nil {
			return out, err
		}
		b, _ := json.Marshal(row)
		if len(out.Entries) >= p.Limit || size+len(b)+1 > MessagePageBytes {
			out.HasMore = true
			break
		}
		out.Entries = append(out.Entries, row)
		size += len(b) + 1
	}
	if len(out.Entries) > 0 {
		last := out.Entries[len(out.Entries)-1]
		b, _ := json.Marshal([2]string{last.Kind, last.Path})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, rows.Err()
}
