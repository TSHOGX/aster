package producthttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// eventPoll bounds how long a subscriber waits between outbox reads. The product
// outbox is the durable source; this stream is a delivery mechanism over it, so a
// missed wakeup delays a projection rather than losing one.
const eventPoll = 250 * time.Millisecond

// events streams product projections for one Project. It reauthorizes on every
// batch: losing access closes this stream rather than granting perpetual rights
// at subscription time.
//
// Terminal bytes are deliberately absent. They travel on the terminal socket, so
// a PTY flood cannot delay Agent completion or lifecycle delivery here.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	scope := s.scope(r)
	after := int64Param(r.URL.Query(), "after")
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if n := int64Param(map[string][]string{"id": {raw}}, "id"); n > 0 {
			after = n
		}
	}
	// Authorize once before committing to a streaming response, so a rejection is
	// an ordinary status code rather than a 200 that immediately ends.
	if _, err := s.Store.Project(r.Context(), scope); err != nil {
		fail(w, err)
		return
	}
	controller := http.NewResponseController(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	first, last, err := s.Store.EventBounds(r.Context(), scope)
	if err != nil {
		return
	}
	if after == 0 || after > last || (first > 0 && after < first-1) {
		kind := "snapshot"
		if after != 0 {
			kind = "gap"
		}
		after = last
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", last, kind, encode(map[string]int64{"cursor": last})); err != nil {
			return
		}
		flusher.Flush()
	}
	ticker := time.NewTicker(eventPoll)
	defer ticker.Stop()
	for {
		actor, err := s.Auth.Actor(r)
		if err != nil || actor != scope.Actor {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		batch, err := s.Store.Events(r.Context(), scope, after, 200)
		if err != nil {
			// Authorization loss and deletion both end the stream. The client
			// reconnects and receives the authoritative refusal as a status code.
			fmt.Fprintf(w, "event: closed\ndata: %s\n\n", encode(map[string]string{"reason": closeReason(err)}))
			flusher.Flush()
			return
		}
		for _, e := range batch {
			after = e.ID
			fmt.Fprintf(w, "id: %d\nevent: projection\ndata: %s\n\n", e.ID, encode(e))
		}
		if len(batch) > 0 {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func closeReason(err error) string {
	_, message := status(err)
	return message
}

func encode(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"projection could not be encoded"}`)
	}
	return raw
}
