package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
)

func pageNumber(r *http.Request, key string, fallback, max int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	if n > max {
		n = max
	}
	return n, nil
}
func messageCursor(r *http.Request, key string) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return n, nil
}
func (s *Server) messageReadScope(w http.ResponseWriter, r *http.Request, id string) (store.MessagePageParams, bool) {
	_, level, visibility, ok := s.authorizeRead(w, r, id)
	if !ok {
		return store.MessagePageParams{}, false
	}
	p := store.MessagePageParams{SessionID: id, Summary: level < accessOwner && visibility != store.ShareVisibilityFull}
	if level == accessOwner {
		p.UserID = userIDFromContext(r.Context())
	}
	return p, true
}
func readPageError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "message not found")
	} else {
		writeError(w, 500, "failed to read message")
	}
}
func (s *Server) listMessagesPage(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := s.messageReadScope(w, r, id)
	if ok {
		s.serveMessagePage(w, r, p)
	}
}
func (s *Server) serveMessagePage(w http.ResponseWriter, r *http.Request, p store.MessagePageParams) {
	var err error
	p.BeforeID, err = messageCursor(r, "before_id")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	p.AfterID, err = messageCursor(r, "after_id")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	p.AroundID, err = messageCursor(r, "around_id")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	cursors := 0
	for _, n := range []int64{p.BeforeID, p.AfterID, p.AroundID} {
		if n > 0 {
			cursors++
		}
	}
	if cursors > 1 {
		writeError(w, 400, "message cursors are mutually exclusive")
		return
	}
	p.Limit, err = pageNumber(r, "limit", 40, 80)
	if err != nil || p.Limit == 0 {
		writeError(w, 400, "invalid limit")
		return
	}
	started := time.Now()
	page, err := s.store.ListMessagesPage(r.Context(), p)
	if err != nil {
		readPageError(w, err)
		return
	}
	w.Header().Set("Server-Timing", fmt.Sprintf("db;dur=%.2f", float64(time.Since(started).Microseconds())/1000))
	if strings.HasPrefix(r.URL.Path, "/api/shared/") {
		messages := make([]map[string]any, 0, len(page.Messages))
		for _, m := range page.Messages {
			messages = append(messages, map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "created_at": m.CreatedAt, "stop_reason": m.StopReason, "content_truncated": m.ContentTruncated, "content_length": m.ContentLength, "has_trace": m.HasTrace, "is_final_reply": m.IsFinalReply})
		}
		writeMeasuredJSON(w, map[string]any{"messages": messages, "has_older": page.HasOlder, "has_newer": page.HasNewer, "before_id": page.BeforeID, "after_id": page.AfterID, "latest_id": page.LatestID, "latest_final_id": page.LatestFinalID, "interruption": page.Interruption})
		return
	}
	writeMeasuredJSON(w, page)
}
func (s *Server) messageContent(w http.ResponseWriter, r *http.Request, id, mid string) {
	p, ok := s.messageReadScope(w, r, id)
	if ok {
		s.serveMessageContent(w, r, p, mid)
	}
}
func (s *Server) serveMessageContent(w http.ResponseWriter, r *http.Request, p store.MessagePageParams, mid string) {
	id, e := strconv.ParseInt(mid, 10, 64)
	if e != nil || id <= 0 {
		writeError(w, 400, "invalid message id")
		return
	}
	offset, e := pageNumber(r, "offset", 0, int(^uint(0)>>1)-8192)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	limit, e := pageNumber(r, "limit", 8192, 8192)
	if e != nil || limit == 0 {
		writeError(w, 400, "invalid limit")
		return
	}
	read := func(offset, limit int) (store.ContentPage, error) {
		return s.store.MessageContent(r.Context(), p, id, offset, limit)
	}
	if strings.HasSuffix(r.URL.Path, "/trace-content") {
		node, err := pageNumber(r, "node", 0, 10000000)
		if err != nil {
			writeError(w, 400, "invalid node")
			return
		}
		read = func(offset, limit int) (store.ContentPage, error) {
			return s.store.TraceContent(r.Context(), p, id, node, offset, limit)
		}
	}
	chunk, e := read(offset, limit)
	if e != nil {
		readPageError(w, e)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="message.txt"`)
		for {
			if _, e = w.Write([]byte(chunk.Content)); e != nil {
				return
			}
			if !chunk.HasMore {
				return
			}
			chunk, e = read(chunk.NextOffset, 8192)
			if e != nil {
				return
			}
		}
	}
	writeJSON(w, 200, chunk)
}
func (s *Server) messageTrace(w http.ResponseWriter, r *http.Request, id, mid string) {
	p, ok := s.messageReadScope(w, r, id)
	if ok {
		s.serveMessageTrace(w, r, p, mid)
	}
}
func (s *Server) serveMessageTrace(w http.ResponseWriter, r *http.Request, p store.MessagePageParams, mid string) {
	id, e := strconv.ParseInt(mid, 10, 64)
	if e != nil || id <= 0 {
		writeError(w, 400, "invalid message id")
		return
	}
	offset, e := pageNumber(r, "offset", 0, 10000000)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	limit, e := pageNumber(r, "limit", 50, 50)
	if e != nil || limit == 0 {
		writeError(w, 400, "invalid limit")
		return
	}
	page, e := s.store.MessageTrace(r.Context(), p, id, offset, limit)
	if e != nil {
		readPageError(w, e)
		return
	}
	writeJSON(w, 200, page)
}

func writeMeasuredJSON(w http.ResponseWriter, value any) {
	started := time.Now()
	body, err := json.Marshal(value)
	if err != nil {
		writeError(w, 500, "cannot encode response")
		return
	}
	w.Header().Add("Server-Timing", fmt.Sprintf("encode;dur=%.2f", float64(time.Since(started).Microseconds())/1000))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
