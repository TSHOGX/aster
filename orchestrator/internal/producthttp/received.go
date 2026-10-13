package producthttp

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/product"
	"io"
	"mime"
	"net/http"
	"path"
	"sync/atomic"
	"time"
)

func (s *Server) receivedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/conversation-copies", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Store.ConversationCopies(r.Context(), actorFrom(r.Context()))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"copies": out})
	})
	mux.HandleFunc("DELETE /api/conversation-copies/{copy}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.CancelConversationCopy(r.Context(), actorFrom(r.Context()), r.PathValue("copy")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/conversations/{session}/files/events", s.receivedFileEvents)
	mux.HandleFunc("POST /api/conversation-copies", func(w http.ResponseWriter, r *http.Request) {
		var body product.CopyRequest
		if !decode(w, r, s.Config.bodyLimit(), &body) {
			return
		}
		out, err := s.Store.CreateConversationCopy(r.Context(), actorFrom(r.Context()), body)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 202, out)
	})
	mux.HandleFunc("GET /api/conversation-copies/{copy}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Store.ConversationCopy(r.Context(), actorFrom(r.Context()), r.PathValue("copy"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/conversation-copies/{copy}/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.RetryConversationCopy(r.Context(), actorFrom(r.Context()), r.PathValue("copy")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/conversations/{session}/files/content", s.receivedFile)
	mux.HandleFunc("GET /api/conversations/{session}/files/download", s.receivedFile)
	mux.HandleFunc("GET /api/conversations/sources", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Store.ReceivedSources(r.Context(), actorFrom(r.Context()), r.URL.Query().Get("after"), intParam(r.URL.Query(), "limit", 50))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"sources": items})
	})
	mux.HandleFunc("GET /api/conversations/events", s.receivedIndexEvents)
	mux.HandleFunc("GET /api/conversations/{session}/view/events", s.receivedViewEvents)
	for suffix, handler := range map[string]http.HandlerFunc{"head": s.conversationHead, "pages": s.conversationPage, "changes": s.conversationChanges, "turns": s.conversationSummaries, "messages/{message}/content": s.conversationMessageText, "items/{item}/detail": s.conversationDetail} {
		mux.HandleFunc("GET /api/conversations/{session}/view/"+suffix, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			handler(w, r.WithContext(product.ReceivedViewContext(r.Context())))
		})
	}
	mux.HandleFunc("GET /api/conversations/{session}/turn-index", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := s.Store.TurnIndex(product.ReceivedViewContext(r.Context()), s.scope(r), r.PathValue("session"), int64Param(q, "before"), int64Param(q, "after"), intParam(q, "limit", 100))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, page)
	})
	mux.HandleFunc("PATCH /api/projects/{project}/sessions/{session}/readers/{reader}", func(w http.ResponseWriter, r *http.Request) {
		var body product.ReaderAccess
		if !decode(w, r, s.Config.bodyLimit(), &body) {
			return
		}
		if err := s.Store.SetReaderAccess(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("reader"), body); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
}

func (s *Server) receivedFileEvents(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	actor, session, name := actorFrom(ctx), r.PathValue("session"), r.URL.Query().Get("path")
	var revision atomic.Int64
	go func() {
		for ctx.Err() == nil {
			watch, err := s.Files.WatchReceived(ctx, actor, session, name)
			if err == nil {
				for {
					_, err = watch.Next()
					if err != nil {
						break
					}
					revision.Add(1)
				}
				watch.Close()
				revision.Add(1)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	s.sharedEvents(w, r, func() (string, error) {
		meta, err := s.Store.ReceivedConversation(ctx, actor, session)
		if err != nil {
			return "", err
		}
		allowed := false
		for _, p := range meta.Paths {
			if p == name {
				allowed = true
			}
		}
		if !allowed {
			return "", product.ErrNotFound
		}
		fileRevision, err := s.Store.ReceivedFileRevision(ctx, actor, session, name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d/%d/%s", meta.Revision, revision.Load(), fileRevision), nil
	})
}

func (s *Server) receivedFile(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	download := path.Base(r.URL.Path) == "download"
	actor, session, name := actorFrom(r.Context()), r.PathValue("session"), r.URL.Query().Get("path")
	metadata, err := s.Store.ReceivedConversation(r.Context(), actor, session)
	if err != nil {
		fail(w, err)
		return
	}
	capture, err := s.Files.CaptureReceived(r.Context(), actor, session, name, download)
	if err != nil {
		fail(w, err)
		return
	}
	defer capture.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	if media := mime.TypeByExtension(path.Ext(name)); media == "image/png" || media == "image/jpeg" || media == "image/gif" || media == "image/webp" || media == "image/avif" {
		w.Header().Set("Content-Type", media)
	}
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(name)}))
	controller := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	checked := time.Time{}
	for {
		if time.Since(checked) > 500*time.Millisecond {
			current, e := s.Store.ReceivedConversation(r.Context(), actor, session)
			if e != nil || current.Revision != metadata.Revision {
				panic(http.ErrAbortHandler)
			}
			checked = time.Now()
		}
		n, e := capture.Read(buf)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(authorityWriteWait))
			if _, err = w.Write(buf[:n]); err != nil {
				return
			}
		}
		if e == io.EOF {
			return
		}
		if e != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// A single lightweight stream invalidates the visible navigation, regardless of
// how many sources are expanded. Bodies are fetched only when revisions change.
func (s *Server) receivedIndexEvents(w http.ResponseWriter, r *http.Request) {
	s.sharedEvents(w, r, func() (string, error) { return s.Store.ReceivedIndexRevision(r.Context(), actorFrom(r.Context())) })
}
func (s *Server) receivedViewEvents(w http.ResponseWriter, r *http.Request) {
	s.sharedEvents(w, r, func() (string, error) {
		metadata, err := s.Store.ReceivedConversation(r.Context(), actorFrom(r.Context()), r.PathValue("session"))
		if err != nil {
			return "", err
		}
		head, err := s.Store.ConversationHead(product.ReceivedViewContext(r.Context()), s.scope(r), r.PathValue("session"))
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal([]any{metadata, head.ContentRevision, head.IndexRevision, head.Generation, head.State})
		return string(raw), nil
	})
}
func (s *Server) sharedEvents(w http.ResponseWriter, r *http.Request, revision func() (string, error)) {
	previous, err := revision()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(authorityWriteWait))
		_, err := fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
		return err == nil && controller.Flush() == nil
	}
	if !send("ready") {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			// Public streams are bearer-authorized by revision; private ones also
			// revalidate the login cookie without relying on a cached request actor.
			if r.PathValue("token") == "" {
				if actor, err := s.Auth.Actor(r); err != nil || actor != actorFrom(r.Context()) {
					send("closed")
					return
				}
			}
			next, err := revision()
			if err != nil {
				send("closed")
				return
			}
			if next != previous {
				previous = next
				if !send("changed") {
					return
				}
			} else {
				_ = controller.SetWriteDeadline(time.Now().Add(authorityWriteWait))
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
					return
				}
			}
		}
	}
}
