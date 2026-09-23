package producthttp

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.Schedules(r.Context(), s.scope(r), q.Get("after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"schedules": items})
}
func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var body product.Schedule
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	item, err := s.Store.CreateSchedule(r.Context(), s.scope(r), body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, item)
}
func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.Schedule(r.Context(), s.scope(r), r.PathValue("schedule"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	var body product.Schedule
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	body.ID = r.PathValue("schedule")
	if err := s.Store.UpdateSchedule(r.Context(), s.scope(r), body, false); err != nil {
		fail(w, err)
		return
	}
	s.getSchedule(w, r)
}
func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.Schedule(r.Context(), s.scope(r), r.PathValue("schedule"))
	if err != nil {
		fail(w, err)
		return
	}
	item.Revision = int64Param(r.URL.Query(), "revision")
	if err = s.Store.UpdateSchedule(r.Context(), s.scope(r), item, true); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	run, err := s.Store.RunSchedule(r.Context(), s.scope(r), r.PathValue("schedule"), body.RequestID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, run)
}
func (s *Server) scheduleRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.ScheduleRuns(r.Context(), s.scope(r), r.PathValue("schedule"), q.Get("after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"runs": items})
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	results, err := s.Store.Search(r.Context(), actorFrom(r.Context()), q.Get("q"), q.Get("after"), intParam(q, "limit", 50))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": results})
}
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.Notifications(r.Context(), actorFrom(r.Context()), q.Get("after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	unread, err := s.Store.UnreadNotifications(r.Context(), actorFrom(r.Context()))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": items, "unread": unread})
}
func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ReadNotification(r.Context(), actorFrom(r.Context()), r.PathValue("notification")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) sharedConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.SharedConversations(r.Context(), actorFrom(r.Context()), q.Get("after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"conversations": items})
}
func (s *Server) conversationMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.ConversationMessages(r.Context(), actorFrom(r.Context()), r.PathValue("session"), int64Param(q, "after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": items})
}
func (s *Server) sharedConversation(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.SharedConversation(r.Context(), actorFrom(r.Context()), r.PathValue("session"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) grantConversation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reader string `json:"reader"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if err := s.Store.GrantConversation(r.Context(), s.scope(r), r.PathValue("session"), body.Reader); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) revokeConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeConversation(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("reader")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) conversationEvents(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	session := r.PathValue("session")
	if err := s.Store.CanReadConversation(r.Context(), actor, session); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	closed := func() {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(w, "event: closed\ndata: {}\n\n")
		_ = controller.Flush()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	after := int64(-1)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
			after = n
		}
	}
	for {
		frontier, err := s.Store.ConversationFrontier(r.Context(), actor, session)
		if err != nil {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.WriteString(w, "event: closed\ndata: {}\n\n")
			_ = controller.Flush()
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		frame := ": heartbeat\n\n"
		if frontier != after {
			frame = fmt.Sprintf("id: %d\nevent: conversation\ndata: {}\n\n", frontier)
			after = frontier
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
		}
		current, err := s.Auth.Actor(r)
		if err != nil || current != actor {
			closed()
			return
		}
		if err = s.Store.CanReadConversation(r.Context(), actor, session); err != nil {
			closed()
			return
		}
	}
}
func (s *Server) sharedConversationHead(w http.ResponseWriter, r *http.Request) {
	frontier, err := s.Store.ConversationFrontier(r.Context(), actorFrom(r.Context()), r.PathValue("session"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content_revision": strconv.FormatInt(frontier, 10)})
}
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	if s.Shares == nil {
		writeError(w, 503, "sharing unavailable")
		return
	}
	var body struct {
		Paths     []string  `json:"paths"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if body.ExpiresAt.IsZero() {
		body.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
	}
	share, token, err := s.Shares.Create(r.Context(), s.scope(r), r.PathValue("session"), body.Paths, body.ExpiresAt)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"share": share, "token": token})
}
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.Shares(r.Context(), s.scope(r), r.PathValue("session"), q.Get("after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"shares": items})
}
func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeShare(r.Context(), s.scope(r), r.PathValue("share")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) rotateShare(w http.ResponseWriter, r *http.Request) {
	token, err := s.Store.RotateShare(r.Context(), s.scope(r), r.PathValue("share"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"token": token})
}
func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	share, err := s.Store.PublicShare(r.Context(), r.PathValue("token"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": share.ID, "title": share.Title, "files": share.Files, "expires_at": share.ExpiresAt})
}
func (s *Server) publicMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.Store.PublicMessages(r.Context(), r.PathValue("token"), int64Param(q, "after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	// The public DTO has no internal/native/engine identity fields.
	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		out = append(out, map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "sequence": m.Sequence, "created_at": m.CreatedAt})
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}
func (s *Server) publicFile(w http.ResponseWriter, r *http.Request) {
	if s.Shares == nil {
		writeError(w, 503, "sharing unavailable")
		return
	}
	token := r.PathValue("token")
	name := r.URL.Query().Get("path")
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	file, meta, err := s.Shares.OpenFile(ctx, token, name)
	if err != nil {
		fail(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(meta.Size))
	controller := http.NewResponseController(w)
	buffer := make([]byte, 32<<10)
	checkAt := time.Time{}
	for {
		if ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		if time.Now().After(checkAt) {
			if _, err := s.Store.PublicShare(ctx, token); err != nil {
				panic(http.ErrAbortHandler)
			}
			checkAt = time.Now().Add(500 * time.Millisecond)
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, e := w.Write(buffer[:n]); e != nil {
				return
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func (s *Server) scheduleOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := s.Store.ScheduleOverviewPage(r.Context(), actorFrom(r.Context()), q.Get("project_id"), q.Get("after"), intParam(q, "limit", 50))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
