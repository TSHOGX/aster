package producthttp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	scope := s.scope(r)
	if _, err := s.Store.Project(r.Context(), scope); err != nil {
		fail(w, err)
		return
	}
	s.streamResources(w, r, scope)
}

// streamResources gives both Project and owner Scheduled streams the same
// bounded transport, rotation, replay and authentication lifetime.
func (s *Server) streamResources(w http.ResponseWriter, r *http.Request, scope product.Scope) {
	cursor := r.URL.Query().Get("cursor")
	if value := r.Header.Get("Last-Event-ID"); value != "" {
		cursor = value
	}
	s.hubsOnce.Do(func() { s.hubs = newResourceHubs(s) })
	if s.hubs == nil {
		writeError(w, http.StatusServiceUnavailable, "application closing")
		return
	}
	sub, err := s.hubs.attach(scope, cursor)
	if err != nil {
		fail(w, err)
		return
	}
	defer s.hubs.detach(sub)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
	if control.Flush() != nil {
		return
	}
	frames := make(chan resourceFrame, 1)
	go func() {
		defer close(frames)
		for {
			frame, e := sub.next(ctx)
			if e != nil {
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	recheck := time.NewTicker(eventPoll)
	defer recheck.Stop()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	rotation := time.NewTimer(25 * time.Second)
	defer rotation.Stop()
	send := func(kind string, frame resourceFrame) bool {
		_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
		_, e := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", frame.event.Cursor, kind, frame.raw)
		return e == nil && control.Flush() == nil
	}
	invalidations := identityInvalidations(s.Auth)
	authorized := func() bool {
		actor, e := s.Auth.Actor(r)
		if e != nil || actor != scope.Actor {
			_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
			fmt.Fprint(w, "event: closed\ndata: {\"reason\":\"authentication\"}\n\n")
			_ = control.Flush()
			return false
		}
		if scope.ProjectID != "" {
			if _, e = s.Store.Project(r.Context(), scope); e != nil {
				_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
				fmt.Fprint(w, "event: closed\ndata: {\"reason\":\"authorization\"}\n\n")
				_ = control.Flush()
				return false
			}
		}

		return true
	}
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if !send("resource", frame) {
				return
			}
			last = frame.event.Cursor
			if frame.event.Kind == "closed" {
				return
			}
		case <-invalidations:
			invalidations = identityInvalidations(s.Auth)
			if !authorized() {
				return
			}
		case <-recheck.C:
			if !authorized() {
				return
			}

		case <-heartbeat.C:
			_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
			if _, e := fmt.Fprint(w, ": heartbeat\n\n"); e != nil || control.Flush() != nil {
				return
			}
		case <-rotation.C:
			raw := encode(map[string]string{"cursor": last})
			_ = control.SetWriteDeadline(time.Now().Add(authorityWriteWait))
			fmt.Fprintf(w, "event: rotate\ndata: %s\n\n", raw)
			_ = control.Flush()
			return
		}
	}
}

// Close releases event hubs before closing the application Store.
func (s *Server) Close() {
	s.hubsOnce.Do(func() {})
	if s.hubs != nil {
		s.hubs.close()
	}
}
