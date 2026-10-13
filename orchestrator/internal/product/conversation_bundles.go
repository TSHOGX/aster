package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// freezeConversation runs in the same transaction as publication/admission.
// Only complete display segments are admitted; no queued prompt or unfinished
// tool call is carried across a share/copy boundary.
func freezeConversation(ctx context.Context, tx *sql.Tx, id, session, title, stamp string) (int64, error) {
	var frontier int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(max(m.sequence),0) FROM messages m JOIN conversation_turns t ON t.session_id=m.session_id AND t.turn_id=m.turn_id WHERE m.session_id=? AND json_extract(t.data,'$.state') IN ('completed','continued','failed','canceled')`, session).Scan(&frontier)
	if err != nil {
		return 0, err
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(content)),0) FROM messages WHERE session_id=? AND sequence<=?`, session, frontier).Scan(&size); err != nil {
		return 0, err
	}
	if size > 64<<20 {
		return 0, ErrCapacity
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_bundles VALUES(?,?,?,?)`, id, title, frontier, stamp); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO bundle_turns SELECT ?,t.id,t.sequence,t.content,json_remove(v.data,'$.run_id','$.segment_id') FROM turns t JOIN conversation_turns v ON v.session_id=t.session_id AND v.turn_id=t.id WHERE t.session_id=? AND json_extract(v.data,'$.state') IN ('completed','continued','failed','canceled') AND EXISTS(SELECT 1 FROM messages m WHERE m.turn_id=t.id AND m.sequence<=?)`, id, session, frontier); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO bundle_messages SELECT ?,m.id,m.turn_id,m.role,m.content,m.sequence,m.created_at FROM messages m JOIN bundle_turns t ON t.bundle_id=? AND t.id=m.turn_id WHERE m.session_id=? AND m.sequence<=?`, id, id, session, frontier); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bundle_items SELECT ?,i.id,i.turn_id,i.position,i.block_index,json_remove(i.data,'$.run_id','$.segment_id'),json_remove(coalesce(d.content,i.data),'$.run_id','$.segment_id') FROM conversation_items i JOIN bundle_turns t ON t.bundle_id=? AND t.id=i.turn_id LEFT JOIN conversation_details d ON d.session_id=i.session_id AND d.item_id=i.id AND d.item_revision=CAST(json_extract(i.data,'$.detail_version') AS INTEGER) WHERE i.session_id=?`, id, id, session)
	if err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(data)+length(detail)),0) FROM bundle_items WHERE bundle_id=?`, id).Scan(&size); err != nil {
		return 0, err
	}
	if size > 64<<20 {
		return 0, ErrCapacity
	}
	return frontier, nil
}

func bundleHead(ctx context.Context, q queryer, id string) (ViewHead, error) {
	head := ViewHead{State: "ready", Generation: id, ContentRevision: 1, IndexRevision: 1, SourceFrontier: "1", ProjectedFrontier: "1", Turns: []Turn{}, Recovery: []AgentRecoveryStatus{}}
	head.Session = Session{ID: id, State: "idle", Revision: 1}
	err := missing(q.QueryRowContext(ctx, `SELECT title,EXISTS(SELECT 1 FROM bundle_items WHERE bundle_id=?) FROM conversation_bundles WHERE id=?`, id, id).Scan(&head.Session.Title, &head.HasContent))
	return head, err
}
func (s *Store) PublicBundle(ctx context.Context, token string) (Share, error) {
	share, err := s.PublicShare(ctx, token)
	if err != nil {
		return share, err
	}
	if share.BundleID == "" {
		return share, ErrPending
	}
	return share, nil
}
func (s *Store) BundleHead(ctx context.Context, token string) (ViewHead, error) {
	share, err := s.PublicBundle(ctx, token)
	if err != nil {
		return ViewHead{}, err
	}
	return bundleHead(ctx, s.reader, share.BundleID)
}
func (s *Store) BundlePage(ctx context.Context, token string, sel ViewSelector) (ViewPage, error) {
	share, err := s.PublicBundle(ctx, token)
	if err != nil {
		return ViewPage{}, err
	}
	return readBundlePage(ctx, s.reader, share.BundleID, sel)
}
func readBundlePage(ctx context.Context, q queryer, id string, sel ViewSelector) (ViewPage, error) {
	out := ViewPage{Items: []DisplayItem{}, Summaries: []TurnSummary{}}
	var err error
	out.Head, err = bundleHead(ctx, q, id)
	if err != nil {
		return out, err
	}
	args := []any{id}
	where := ""
	order := "DESC"
	choices := 0
	for _, v := range []string{sel.Before, sel.After, sel.AroundTurn, sel.AroundItem, sel.AroundMessage} {
		if v != "" {
			choices++
		}
	}
	if choices > 1 {
		return out, ErrInvalid
	}
	if sel.Before != "" || sel.After != "" {
		raw, op := sel.Before, "<"
		if sel.After != "" {
			raw, op, order = sel.After, ">", "ASC"
		}
		at, e := decodeViewToken(raw, "page", id, id)
		if e != nil {
			return out, e
		}
		where = " AND (position,block_index,id)" + op + "(?,?,?)"
		args = append(args, at.Position, at.Block, at.ID)
	} else if choices > 0 {
		column, value := "turn_id", sel.AroundTurn
		if sel.AroundItem != "" {
			column, value = "id", sel.AroundItem
		}
		if sel.AroundMessage != "" {
			column, value = "json_extract(data,'$.message_id')", sel.AroundMessage
		}
		var position int64
		var block int
		var item string
		if err = q.QueryRowContext(ctx, `SELECT position,block_index,id FROM bundle_items WHERE bundle_id=? AND `+column+`=? ORDER BY position,block_index,id LIMIT 1`, id, value).Scan(&position, &block, &item); err != nil {
			return out, missing(err)
		}
		where = " AND (position,block_index,id)>=(?,?,?)"
		args = append(args, position, block, item)
		order = "ASC"
	}
	rows, err := q.QueryContext(ctx, `SELECT data FROM bundle_items WHERE bundle_id=?`+where+` ORDER BY position `+order+`,block_index `+order+`,id `+order+` LIMIT 50`, args...)
	if err != nil {
		return out, err
	}
	size := 32 << 10
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if size+len(raw) > displayResponseBytes {
			break
		}
		var item DisplayItem
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			break
		}
		out.Items = append(out.Items, item)
		size += len(raw)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if order == "DESC" {
		for a, b := 0, len(out.Items)-1; a < b; a, b = a+1, b-1 {
			out.Items[a], out.Items[b] = out.Items[b], out.Items[a]
		}
	}
	seen := map[string]bool{}
	for _, item := range out.Items {
		if seen[item.TurnID] {
			continue
		}
		seen[item.TurnID] = true
		var raw string
		var summary TurnSummary
		if err = q.QueryRowContext(ctx, `SELECT data FROM bundle_turns WHERE bundle_id=? AND id=?`, id, item.TurnID).Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(raw), &summary); err != nil {
			return out, err
		}
		out.Summaries = append(out.Summaries, summary)
	}
	if len(out.Items) > 0 {
		first, last := out.Items[0], out.Items[len(out.Items)-1]
		var before, after bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bundle_items WHERE bundle_id=? AND (position,block_index,id)<(?,?,?)),EXISTS(SELECT 1 FROM bundle_items WHERE bundle_id=? AND (position,block_index,id)>(?,?,?))`, id, first.Position, first.Block, first.ID, id, last.Position, last.Block, last.ID).Scan(&before, &after); err != nil {
			return out, err
		}
		cursor := func(item DisplayItem) string {
			return encodeViewToken(viewToken{Kind: "page", Session: id, Generation: id, Position: item.Position, Block: item.Block, ID: item.ID})
		}
		if before {
			out.Before = cursor(first)
		}
		if after {
			out.After = cursor(last)
		}
		out.Window = cursor(first)
	}
	return out, nil
}
func (s *Store) BundleText(ctx context.Context, token, item string, message bool, cursor string) (ViewDetail, error) {
	share, err := s.PublicBundle(ctx, token)
	if err != nil {
		return ViewDetail{}, err
	}
	id := share.BundleID
	at := viewToken{Kind: "bundle-text", Session: id, Generation: id, ID: item}
	if cursor != "" {
		at, err = decodeViewToken(cursor, "bundle-text", id, id)
		if err != nil || at.ID != item {
			return ViewDetail{}, ErrInvalid
		}
	}
	column, table := "detail", "bundle_items"
	if message {
		column, table = "content", "bundle_messages"
	}
	var raw []byte
	var total int
	err = s.reader.QueryRowContext(ctx, fmt.Sprintf(`SELECT substr(CAST(%s AS BLOB),?,?),length(CAST(%s AS BLOB)) FROM %s WHERE bundle_id=? AND id=?`, column, column, table), at.Offset+1, 32<<10, id, item).Scan(&raw, &total)
	if err != nil {
		return ViewDetail{}, missing(err)
	}
	if at.Offset > total {
		return ViewDetail{}, ErrInvalid
	}
	end := len(raw)
	for end > 0 && !utf8.Valid(raw[:end]) {
		end--
	}
	out := ViewDetail{Version: "1", Data: string(raw[:end])}
	if at.Offset+end < total {
		if end == 0 {
			return out, ErrInvalid
		}
		at.Offset += end
		out.Next = encodeViewToken(at)
	}
	return out, nil
}
func (s *Store) BundleTurns(ctx context.Context, token string, ids []string) (ViewSummaries, error) {
	share, err := s.PublicBundle(ctx, token)
	out := ViewSummaries{Generation: share.BundleID, Revision: 1, Turns: []TurnSummary{}}
	if err != nil {
		return out, err
	}
	if len(ids) > 50 {
		return out, ErrInvalid
	}
	for _, id := range ids {
		var raw string
		err = s.reader.QueryRowContext(ctx, `SELECT data FROM bundle_turns WHERE bundle_id=? AND id=?`, share.BundleID, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		var t TurnSummary
		if err = json.Unmarshal([]byte(raw), &t); err != nil {
			return out, err
		}
		out.Turns = append(out.Turns, t)
	}
	return out, nil
}
func (s *Store) BundleIndex(ctx context.Context, token string, before, after int64, limit int) (TurnIndexPage, error) {
	share, err := s.PublicBundle(ctx, token)
	out := TurnIndexPage{Turns: []TurnIndexItem{}}
	if err != nil {
		return out, err
	}
	if before < 0 || after < 0 || before > 0 && after > 0 {
		return out, ErrInvalid
	}
	if limit < 1 || limit > 300 {
		limit = 100
	}
	order := "DESC"
	if after > 0 {
		order = "ASC"
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,sequence,json_extract(data,'$.state'),coalesce(json_extract(data,'$.created_at'),''),substr(content,1,600) FROM bundle_turns WHERE bundle_id=? AND (?=0 OR sequence<?) AND sequence>? ORDER BY sequence `+order+` LIMIT ?`, share.BundleID, before, before, after, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var t TurnIndexItem
		if err = rows.Scan(&t.ID, &t.Sequence, &t.State, &t.CreatedAt, &t.UserPreview); err != nil {
			return out, err
		}
		out.Turns = append(out.Turns, t)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	if after == 0 {
		for a, b := 0, len(out.Turns)-1; a < b; a, b = a+1, b-1 {
			out.Turns[a], out.Turns[b] = out.Turns[b], out.Turns[a]
		}
	}
	if len(out.Turns) > 0 {
		first, last := out.Turns[0].Sequence, out.Turns[len(out.Turns)-1].Sequence
		var older, newer bool
		if err = s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bundle_turns WHERE bundle_id=? AND sequence<?),EXISTS(SELECT 1 FROM bundle_turns WHERE bundle_id=? AND sequence>?)`, share.BundleID, first, share.BundleID, last).Scan(&older, &newer); err != nil {
			return out, err
		}
		if older {
			out.NextBefore = first
		}
		if newer {
			out.NextAfter = last
		}
	}
	return out, nil
}
