package store

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Snippet markers wrap the matched span inside a message snippet returned by
// SearchMessages. They are deliberately uncommon codepoints so the frontend can
// locate and highlight the match without colliding with real message text.
const (
	SearchSnippetOpen  = "⟦" // ⟦
	SearchSnippetClose = "⟧" // ⟧
)

// minTrigramRunes is the shortest query the FTS5 trigram tokenizer can match:
// a trigram index has no tokens shorter than three characters, so queries with
// fewer runes fall back to a title-only LIKE scan instead of erroring out.
const minTrigramRunes = 3

// SearchHit is a single search result. Kind is "title" for a session whose
// title matched, or "message" for a message body match. MessageID, Role and
// Snippet are only populated for message hits.
type SearchHit struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	MessageID *int64 `json:"message_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Snippet   string `json:"snippet"`
	CreatedAt string `json:"created_at"`
}

// SearchMessages searches a user's session titles and message bodies (both user
// and assistant messages) for the given query. Title hits are returned first,
// then message hits ordered by FTS5 relevance (bm25). Results are always scoped
// to userID so a caller can never see another user's data. The combined result
// is capped at limit.
//
// Queries with fewer than three runes skip the trigram FTS index (which cannot
// tokenize spans shorter than a trigram) and match on titles only.
func (s *Store) SearchMessages(ctx context.Context, userID, query string, limit int) ([]SearchHit, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" || limit <= 0 {
		return []SearchHit{}, nil
	}

	titleHits, err := s.searchTitles(ctx, userID, trimmed, limit)
	if err != nil {
		return nil, err
	}

	hits := titleHits
	// Short queries cannot be tokenized by the trigram index; title-only.
	if utf8.RuneCountInString(trimmed) >= minTrigramRunes && len(hits) < limit {
		messageHits, err := s.searchMessageBodies(ctx, userID, trimmed, limit-len(hits))
		if err != nil {
			return nil, err
		}
		hits = append(hits, messageHits...)
	}

	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// searchTitles returns one hit per session whose title contains the query as a
// substring (case-sensitive LIKE, matching SQLite's default). The query is
// escaped for LIKE so %, _ and \ are treated literally.
func (s *Store) searchTitles(ctx context.Context, userID, query string, limit int) ([]SearchHit, error) {
	pattern := "%" + escapeLike(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, status, mode, created_at
FROM sessions
WHERE user_id = ?
  AND title LIKE ? ESCAPE '\'
ORDER BY created_at DESC
LIMIT ?`, userID, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var title, status, mode, createdAt string
		if err := rows.Scan(&h.SessionID, &title, &status, &mode, &createdAt); err != nil {
			return nil, err
		}
		h.Kind = "title"
		h.Title = title
		h.Status = status
		h.Mode = mode
		h.Snippet = title
		h.CreatedAt = createdAt
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// searchMessageBodies matches message content via the trigram FTS index, joins
// back to messages and sessions, scopes to userID, and orders by bm25
// relevance. The snippet wraps the matched span in the SearchSnippet markers so
// the frontend can highlight it; long content is clipped to keep payloads small.
func (s *Store) searchMessageBodies(ctx context.Context, userID, query string, limit int) ([]SearchHit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.session_id, m.role, m.created_at,
       s.title, s.status, s.mode,
       snippet(messages_fts, 0, ?, ?, '…', 12)
FROM messages_fts
JOIN messages m ON m.id = messages_fts.rowid
JOIN sessions s ON s.id = m.session_id
WHERE messages_fts MATCH ?
  AND s.user_id = ?
ORDER BY bm25(messages_fts)
LIMIT ?`, SearchSnippetOpen, SearchSnippetClose, ftsMatchQuery(query), userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var id int64
		var role, createdAt, title, status, mode, snippet string
		if err := rows.Scan(&id, &h.SessionID, &role, &createdAt, &title, &status, &mode, &snippet); err != nil {
			return nil, err
		}
		msgID := id
		h.Kind = "message"
		h.MessageID = &msgID
		h.Role = role
		h.Title = title
		h.Status = status
		h.Mode = mode
		h.Snippet = snippet
		h.CreatedAt = createdAt
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// ftsMatchQuery turns arbitrary user text into a single FTS5 string token. We
// wrap the whole query in double quotes (doubling any internal double-quotes)
// so FTS5 treats it as one literal phrase rather than a query expression; with
// the trigram tokenizer a quoted phrase matches the text as a substring, and
// this neutralizes MATCH operators (AND/OR/NEAR/*/-) that would otherwise let a
// user craft a syntax error or unexpected query.
func ftsMatchQuery(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}
