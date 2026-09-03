package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	sessionID := r.URL.Query().Get("session_id")
	if sessionID != "" && !s.sessionVisibleTo(r.Context(), sessionID, userID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, cancel := s.hub.Subscribe(sessionID)
	defer cancel()

	for {
		select {
		case <-s.shutdownSignal():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if !s.eventVisibleTo(r.Context(), event, userID) {
				continue
			}
			if err := conn.WriteJSON(s.publicEventForDelivery(event)); err != nil {
				return
			}
		}
	}
}

func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	userID := userIDFromContext(r.Context())
	sessionID := r.URL.Query().Get("session_id")
	if sessionID != "" && !s.sessionVisibleTo(r.Context(), sessionID, userID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastEventID, cursorProvided, err := parseLastEventID(r)
	if err != nil {
		s.writeSSEError(w, flusher, "invalid_last_event_id", err.Error())
		return
	}
	ch, cancel := s.hub.Subscribe(sessionID)
	defer cancel()

	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()

	replayedThrough := lastEventID
	if cursorProvided {
		if nextAfter, err := s.writeSSEReplay(r.Context(), w, flusher, sessionID, lastEventID, userID); err != nil {
			s.log.Warn("failed to replay stream events", "session_id", sessionID, "last_event_id", lastEventID, "error", err)
			return
		} else if nextAfter > replayedThrough {
			replayedThrough = nextAfter
		}
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-s.shutdownSignal():
			return
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			if event.EventID != 0 && event.EventID <= replayedThrough {
				continue
			}
			if !s.eventVisibleTo(r.Context(), event, userID) {
				if event.EventID > replayedThrough {
					replayedThrough = event.EventID
				}
				continue
			}
			if err := s.writeSSEEvent(w, event); err != nil {
				return
			}
			if event.EventID > replayedThrough {
				replayedThrough = event.EventID
			}
			flusher.Flush()
		}
	}
}

// eventVisibleTo decides whether an event may be delivered to userID. Events
// not tied to a session (global/system) are always visible; session-scoped
// events require ownership.
func (s *Server) eventVisibleTo(ctx context.Context, event events.Event, userID string) bool {
	if strings.TrimSpace(event.SessionID) == "" {
		return true
	}
	return s.sessionVisibleTo(ctx, event.SessionID, userID)
}

// sessionVisibleTo reports whether sessionID is owned by userID, using a short
// TTL cache to avoid a DB hit per streamed event. In open mode every session is
// owned by labUserID, so this still holds.
func (s *Server) sessionVisibleTo(ctx context.Context, sessionID, userID string) bool {
	owner, ok := s.cachedSessionOwner(sessionID)
	if !ok {
		session, err := s.store.GetSession(ctx, sessionID)
		if err != nil {
			// Unknown/deleted session: don't leak; allow only if not auth-gated.
			return !s.creds.enabled()
		}
		owner = session.UserID
		s.storeSessionOwner(sessionID, owner)
	}
	return owner == userID
}

func (s *Server) cachedSessionOwner(sessionID string) (string, bool) {
	s.sessionOwnerMu.Lock()
	defer s.sessionOwnerMu.Unlock()
	entry, ok := s.sessionOwnerCache[sessionID]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.userID, true
}

func (s *Server) storeSessionOwner(sessionID, userID string) {
	s.sessionOwnerMu.Lock()
	defer s.sessionOwnerMu.Unlock()
	if s.sessionOwnerCache == nil {
		s.sessionOwnerCache = make(map[string]sessionOwnerEntry)
	}
	s.sessionOwnerCache[sessionID] = sessionOwnerEntry{
		userID:    userID,
		expiresAt: time.Now().Add(30 * time.Second),
	}
}

func (s *Server) writeSSEReplay(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sessionID string, lastEventID int64, userID string) (int64, error) {
	replayAfter := lastEventID
	if oldest, ok, err := s.store.OldestEventID(ctx, sessionID); err != nil {
		return replayAfter, err
	} else if ok && lastEventID < oldest-1 {
		gapID := oldest - 1
		payloadSessionID := any(nil)
		if sessionID != "" {
			payloadSessionID = sessionID
		}
		if err := s.writeSSEEventUncached(w, events.Event{
			EventID: gapID,
			Type:    "replay_gap",
			Payload: map[string]any{
				"requested_last_event_id": lastEventID,
				"oldest_available":        oldest,
				"session_id_filter":       payloadSessionID,
				"reason":                  "retention_window_exceeded",
			},
		}); err != nil {
			return replayAfter, err
		}
		flusher.Flush()
		replayAfter = 0
	}
	records, err := s.store.ListEvents(ctx, store.ListEventsParams{
		AfterEventID: replayAfter,
		SessionID:    sessionID,
	})
	if err != nil {
		return replayAfter, err
	}
	replayedThrough := replayAfter
	for _, record := range records {
		event := eventFromRecord(record)
		if !s.eventVisibleTo(ctx, event, userID) {
			replayedThrough = record.EventID
			continue
		}
		if err := s.writeSSEEventUncached(w, event); err != nil {
			return replayedThrough, err
		}
		replayedThrough = record.EventID
	}
	if len(records) > 0 {
		flusher.Flush()
	}
	return replayedThrough, nil
}

func eventFromRecord(record store.EventRecord) events.Event {
	return events.Event{
		EventID:        record.EventID,
		Type:           record.Type,
		SessionID:      record.SessionID,
		TurnID:         record.TurnID,
		GenerationID:   record.GenerationID,
		OutputSequence: record.OutputSequence,
		ProxyRequestID: record.ProxyRequestID,
		Stream:         record.Stream,
		Severity:       record.Severity,
		Time:           record.CreatedAt,
		Payload:        record.Payload,
	}
}

func parseLastEventID(r *http.Request) (int64, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if raw == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, true, fmt.Errorf("last_event_id must be a non-negative integer")
	}
	return id, true, nil
}

// writeSSEEvent sanitizes and frames a live durable event, reusing the shared
// per-event sanitized payload across subscribers (see publicEventForDelivery).
// Only genuine durable records (unique, immutable event id -> payload) reach
// this path.
func (s *Server) writeSSEEvent(w http.ResponseWriter, event events.Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	return writeSSEEventFramed(w, s.publicEventForDelivery(event))
}

// writeSSEEventUncached sanitizes and frames an event without consulting the
// shared payload cache. It is used for replayed and synthetic events (e.g.
// replay_gap, whose EventID is borrowed from the durable stream and would
// otherwise collide with a real record's cached payload).
func (s *Server) writeSSEEventUncached(w http.ResponseWriter, event events.Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	return writeSSEEventFramed(w, publicEvent(event))
}

func writeSSEEventFramed(w http.ResponseWriter, event events.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.EventID > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", event.EventID); err != nil {
			return err
		}
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func (s *Server) writeSSEError(w http.ResponseWriter, flusher http.Flusher, errorClass, message string) {
	_ = s.writeSSEEvent(w, events.Event{
		Type: "error",
		Payload: map[string]string{
			"error_class": errorClass,
			"error":       message,
		},
	})
	flusher.Flush()
}
