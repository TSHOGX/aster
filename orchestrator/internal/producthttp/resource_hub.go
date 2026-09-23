package producthttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

const hubRingBytes = 4 << 20
const hubRingCount = 4096
const subscriberBytes = 1 << 20
const subscriberCount = 256
const hubIdleGrace = 30 * time.Second

type ResourceEvent struct {
	Protocol    int    `json:"protocol_version"`
	Incarnation string `json:"stream_incarnation"`
	Cursor      string `json:"cursor"`
	Kind        string `json:"resource_kind"`
	ID          string `json:"resource_id,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Payload     any    `json:"payload,omitempty"`
}
type resourceCursor struct {
	Project     string `json:"p"`
	Incarnation string `json:"i"`
	Sequence    uint64 `json:"s,string"`
	Protocol    int    `json:"v"`
}
type resourceFrame struct {
	event    ResourceEvent
	raw      []byte
	sequence uint64
	at       time.Time
}
type resourceHub struct {
	mu          sync.Mutex
	scope       product.Scope
	incarnation string
	sequence    uint64
	outbox      int64
	ring        []resourceFrame
	bytes       int
	subscribers map[*resourceSubscription]struct{}
	cancel      context.CancelFunc
	idle        *time.Timer
	closed      bool
}
type resourceSubscription struct {
	hub    *resourceHub
	wake   chan struct{}
	queue  []resourceFrame
	bytes  int
	closed bool
}

type ResourceHubStats struct {
	Hubs, Subscribers, ReplayBytes, QueuedBytes int
	Watchers                                    int64
}

func (s *Server) ResourceHubStats() ResourceHubStats {
	s.hubsOnce.Do(func() { s.hubs = newResourceHubs(s) })
	if s.hubs == nil {
		return ResourceHubStats{}
	}
	m := s.hubs
	m.mu.Lock()
	defer m.mu.Unlock()
	out := ResourceHubStats{Hubs: len(m.groups), Watchers: m.watches.Load()}
	for _, h := range m.groups {
		h.mu.Lock()
		out.Subscribers += len(h.subscribers)
		out.ReplayBytes += h.bytes
		for sub := range h.subscribers {
			out.QueuedBytes += sub.bytes
		}
		h.mu.Unlock()
	}
	return out
}

type resourceHubs struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	groups  map[product.Scope]*resourceHub
	server  *Server
	group   sync.WaitGroup
	closed  bool
	watches atomic.Int64
}

func newResourceHubs(s *Server) *resourceHubs {
	ctx, cancel := context.WithCancel(context.Background())
	return &resourceHubs{ctx: ctx, cancel: cancel, groups: map[product.Scope]*resourceHub{}, server: s}
}
func (h *resourceHub) cursor(sequence uint64) string {
	raw, _ := json.Marshal(resourceCursor{Project: h.scope.ProjectID, Incarnation: h.incarnation, Sequence: sequence, Protocol: 1})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func (h *resourceHub) frame(kind, id, revision, reason string, payload any) resourceFrame {
	event := ResourceEvent{Protocol: 1, Incarnation: h.incarnation, Cursor: h.cursor(h.sequence), Kind: kind, ID: id, Revision: revision, Reason: reason, Payload: payload}
	raw, _ := json.Marshal(event)
	return resourceFrame{event: event, raw: raw, sequence: h.sequence, at: time.Now()}
}
func (h *resourceHub) prune(now time.Time) {
	for len(h.ring) > 0 && (h.bytes > hubRingBytes || len(h.ring) > hubRingCount || now.Sub(h.ring[0].at) > 90*time.Second) {
		h.bytes -= len(h.ring[0].raw)
		h.ring[0] = resourceFrame{}
		h.ring = h.ring[1:]
	}
}
func (h *resourceHub) publish(kind, id, revision, reason string, payload any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.sequence++
	frame := h.frame(kind, id, revision, reason, payload)
	// File paths are bounded upstream; any unexpectedly large envelope becomes a
	// scoped resync rather than consuming a subscriber's entire budget.
	if len(frame.raw) > 64<<10 {
		frame = h.frame("resync", "", "", "oversized_event", nil)
	}
	h.ring = append(h.ring, frame)
	h.bytes += len(frame.raw)
	h.prune(time.Now())
	for sub := range h.subscribers {
		if sub.closed {
			continue
		}
		if sub.bytes+len(frame.raw) > subscriberBytes || len(sub.queue) >= subscriberCount {
			reset := h.frame("resync", "", "", "slow_consumer", nil)
			sub.queue = []resourceFrame{reset}
			sub.bytes = len(reset.raw)
			sub.closed = true
		} else {
			sub.queue = append(sub.queue, frame)
			sub.bytes += len(frame.raw)
		}
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}
func (h *resourceHub) attach(cursor string) (*resourceSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, product.ErrPending
	}
	if len(h.subscribers) >= 64 {
		return nil, product.ErrCapacity
	}
	if h.idle != nil {
		h.idle.Stop()
		h.idle = nil
	}
	h.prune(time.Now())
	sub := &resourceSubscription{hub: h, wake: make(chan struct{}, 1)}
	reason := "initial"
	var after uint64
	if cursor != "" {
		var position resourceCursor
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if len(cursor) > 2048 || err != nil || json.Unmarshal(raw, &position) != nil || position.Project != h.scope.ProjectID || position.Protocol != 1 {
			return nil, product.ErrInvalid
		}
		after = position.Sequence
		reason = ""
		switch {
		case position.Incarnation != h.incarnation:
			reason = "hub_restart"
		case after > h.sequence:
			reason = "cursor_ahead"
		case len(h.ring) > 0 && after < h.ring[0].sequence-1:
			reason = "replay_expired"
		case len(h.ring) == 0 && after < h.sequence:
			reason = "replay_expired"
		}
	}
	if reason != "" {
		sub.queue = append(sub.queue, h.frame("resync", "", "", reason, nil))
	} else {
		for _, frame := range h.ring {
			if frame.sequence > after {
				sub.queue = append(sub.queue, frame)
			}
		}
	}
	for _, f := range sub.queue {
		sub.bytes += len(f.raw)
	}
	if len(sub.queue) >= subscriberCount || sub.bytes > subscriberBytes-2048 {
		sub.queue = []resourceFrame{h.frame("resync", "", "", "replay_capacity", nil)}
		sub.bytes = len(sub.queue[0].raw)
	}
	// ready is a transport event, never a content invalidation. Attachment and
	// replay happen under the same lock as publication, covering bootstrap races.
	ready := h.frame("ready", "", "", "", nil)
	sub.queue = append(sub.queue, ready)
	sub.bytes += len(ready.raw)
	h.subscribers[sub] = struct{}{}
	return sub, nil
}
func (s *resourceSubscription) next(ctx context.Context) (resourceFrame, error) {
	for {
		s.hub.mu.Lock()
		if len(s.queue) > 0 {
			f := s.queue[0]
			s.queue[0] = resourceFrame{}
			s.queue = s.queue[1:]
			s.bytes -= len(f.raw)
			s.hub.mu.Unlock()
			return f, nil
		}
		closed := s.closed
		s.hub.mu.Unlock()
		if closed {
			return resourceFrame{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return resourceFrame{}, ctx.Err()
		case <-s.wake:
		}
	}
}
func (m *resourceHubs) attach(scope product.Scope, cursor string) (*resourceSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, product.ErrPending
	}
	h := m.groups[scope]
	if h == nil {
		if len(m.groups) >= 128 {
			return nil, product.ErrCapacity
		}
		_, baseline, err := m.server.Store.EventBounds(m.ctx, scope)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(m.ctx)
		h = &resourceHub{scope: scope, incarnation: uuid.NewString(), outbox: baseline, subscribers: map[*resourceSubscription]struct{}{}, cancel: cancel}
		m.groups[scope] = h
		m.group.Add(1)
		go func() { defer m.group.Done(); m.run(ctx, h) }()
	}
	sub, err := h.attach(cursor)
	if err != nil {
		m.scheduleIdle(h)
	}
	return sub, err
}
func (m *resourceHubs) detach(sub *resourceSubscription) {
	h := sub.hub
	h.mu.Lock()
	delete(h.subscribers, sub)
	sub.closed = true
	sub.queue = nil
	sub.bytes = 0
	h.mu.Unlock()
	m.scheduleIdle(h)
}
func (m *resourceHubs) scheduleIdle(h *resourceHub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.subscribers) > 0 || h.idle != nil {
		return
	}
	h.idle = time.AfterFunc(hubIdleGrace, func() {
		h.mu.Lock()
		if len(h.subscribers) > 0 {
			h.mu.Unlock()
			return
		}
		h.closed = true
		h.cancel()
		h.mu.Unlock()
	})
}
func (m *resourceHubs) close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.group.Wait()
}
func (m *resourceHubs) run(ctx context.Context, h *resourceHub) {
	defer func() {
		h.mu.Lock()
		h.closed = true
		if h.idle != nil {
			h.idle.Stop()
		}
		for sub := range h.subscribers {
			sub.closed = true
			select {
			case sub.wake <- struct{}{}:
			default:
			}
		}
		h.mu.Unlock()
		m.mu.Lock()
		if m.groups[h.scope] == h {
			delete(m.groups, h.scope)
		}
		m.mu.Unlock()
	}()
	after := h.outbox
	// Baseline is captured before the first subscriber can finish its resync reads.
	// A later event always appears in a poll after this baseline.
	ticker := time.NewTicker(eventPoll)
	defer ticker.Stop()
	var watch sandbox.FileWatch
	var watchCancel context.CancelFunc
	frames := make(chan sandbox.FileInvalidation, 16)
	failures := make(chan error, 1)
	var watcher sync.WaitGroup
	stopWatch := func() {
		if watchCancel != nil {
			watchCancel()
			watchCancel = nil
		}
		if watch != nil {
			_ = watch.Close()
			watch = nil
			m.watches.Add(-1)
		}
		watcher.Wait()
		for {
			select {
			case <-frames:
				continue
			default:
			}
			break
		}
		for {
			select {
			case <-failures:
				continue
			default:
			}
			break
		}
	}
	defer stopWatch()
	var epoch uint64
	var fileState string
	retryAt := time.Time{}
	poll := true
	for {
		if poll {
			poll = false
			project, e := m.server.Store.Project(ctx, h.scope)
			if e != nil || project.State == "deleted" || project.State == "deleting" {
				h.publish("closed", "", "", "authority_lost", nil)
				return
			}
			if m.server.Files != nil {
				if project.State != "active" {
					if fileState != project.State {
						stopWatch()
						fileState = project.State
						h.publish("files", "", "", "lifecycle", sandbox.FileInvalidation{Reset: true, Reason: "lifecycle"})
					}
				} else if watch == nil && !time.Now().Before(retryAt) {
					stopWatch()
					wctx, cancel := context.WithCancel(ctx)
					next, e := m.server.Files.Watch(wctx, h.scope, 0)
					if e != nil {
						cancel()
						retryAt = time.Now().Add(time.Second)
						if fileState != "unavailable" {
							fileState = "unavailable"
							h.publish("files", "", "", "unavailable", sandbox.FileInvalidation{Reset: true, Reason: "unavailable"})
						}
					} else {
						watch = next
						m.watches.Add(1)
						watchCancel = cancel
						fileState = "active"
						h.publish("files", "", "", "watch_attached", sandbox.FileInvalidation{Reset: true, Reason: "watch_attached"})
						watcher.Add(1)
						go func(w sandbox.FileWatch) {
							defer watcher.Done()
							for {
								event, e := w.Next()
								if e != nil {
									select {
									case failures <- e:
									case <-wctx.Done():
									}
									return
								}
								select {
								case frames <- event:
								case <-wctx.Done():
									return
								}
							}
						}(next)
					}
				}
			}
			pruned, e := m.server.Store.EventRetentionFrontier(ctx, h.scope)
			if e != nil {
				h.publish("closed", "", "", "authority_lost", nil)
				return
			}
			if after < pruned {
				after = pruned
				h.publish("resync", "", "", "outbox_expired", nil)
			}
			batch, e := m.server.Store.Events(ctx, h.scope, after, 200)
			if e != nil {
				h.publish("closed", "", "", "authority_lost", nil)
				return
			}
			// Coalesce only disposable revision hints inside this bounded polling batch.
			hints := map[string]product.Event{}
			order := []string{}
			for _, event := range batch {
				after = event.ID
				kind, id := resourceKind(event)
				if kind == "" {
					continue
				}
				key := kind + "/" + id
				if _, ok := hints[key]; !ok {
					order = append(order, key)
				}
				hints[key] = event
			}
			for _, key := range order {
				e := hints[key]
				kind, id := resourceKind(e)
				var payload any
				if kind == "files" {
					payload = sandbox.FileInvalidation{Reset: true, Reason: "mutation"}
				}
				h.publish(kind, id, strconv.FormatInt(e.Revision, 10), e.Kind, payload)
			}
		}
		select {
		case <-ctx.Done():
			return
		case event := <-frames:
			if epoch != 0 && event.Epoch != epoch {
				event.Reset = true
				event.Reason = "placement"
			}
			epoch = event.Epoch
			h.publish("files", "", strconv.FormatUint(event.Sequence, 10), event.Reason, event)
		case e := <-failures:
			if !errors.Is(e, context.Canceled) {
				h.publish("files", "", "", "watch_gap", sandbox.FileInvalidation{Reset: true, Reason: "watch_gap"})
			}
			stopWatch()
			retryAt = time.Now().Add(time.Second)
		case <-ticker.C:
			poll = true
		}
	}
}
func resourceKind(e product.Event) (string, string) {
	switch {
	case e.Kind == "files.changed":
		return "files", e.ProjectID
	case e.Kind == "conversation.view":
		return "content", e.SessionID
	case e.Kind == "session.title" || e.Kind == "session.created":
		return "sessions", e.SessionID
	case strings.HasPrefix(e.Kind, "session.") || strings.HasPrefix(e.Kind, "turn.") || strings.HasPrefix(e.Kind, "command."):
		return "control", e.SessionID
	case strings.HasPrefix(e.Kind, "terminal."):
		return "terminals", e.TerminalID
	case strings.HasPrefix(e.Kind, "conversation."):
		return "authorization", e.SessionID
	case strings.HasPrefix(e.Kind, "project."):
		return "project", e.ProjectID
	case strings.HasPrefix(e.Kind, "schedule."):
		return "schedules", e.ProjectID
	case strings.HasPrefix(e.Kind, "share."):
		return "shares", e.ProjectID
	default:
		return "resync", ""
	}
}
