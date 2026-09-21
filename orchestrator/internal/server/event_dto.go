package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"harness-platform/orchestrator/internal/events"
)

func publicEvent(event events.Event) events.Event {
	event.GenerationID = ""
	event.Payload = publicEventPayload(event.Payload)
	return event
}

// publicEventPayloadCacheCap bounds the per-server sanitized-payload cache. A
// single durable event fans out to every SSE subscriber of its
// session within a short window; the cap only needs to comfortably exceed the
// number of distinct event IDs in flight across all sessions at once. Entries
// are evicted in insertion (== ascending event-id) order.
const publicEventPayloadCacheCap = 512

// publicPayloadCache memoizes the sanitized (public) form of an event payload
// keyed by its durable event id. sanitizePublicEventValue is a pure function of
// the payload, and a durable event id maps to exactly one immutable stored
// record, so the cached value is identical for every subscriber of that event.
// Live-only events (id 0, e.g. normalized agent.delta) are never cached here
// because they carry no stable shared identity; callers compute those directly.
type publicPayloadCache struct {
	mu      sync.Mutex
	entries map[int64]any
	order   []int64
}

func (c *publicPayloadCache) get(id int64) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[id]
	return value, ok
}

func (c *publicPayloadCache) put(id int64, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]any, publicEventPayloadCacheCap)
	}
	if _, exists := c.entries[id]; exists {
		c.entries[id] = value
		return
	}
	if len(c.order) >= publicEventPayloadCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[id] = value
	c.order = append(c.order, id)
}

// publicEventForDelivery returns the client-facing form of event, reusing the
// sanitized payload across every subscriber of the same durable event instead
// of running the marshal/unmarshal + sanitize walk once per connection. The
// output bytes are identical to publicEvent(event); only the redundant
// recomputation is removed.
func (s *Server) publicEventForDelivery(event events.Event) events.Event {
	event.GenerationID = ""
	event.Payload = s.publicEventPayloadForDelivery(event.EventID, event.Payload)
	return event
}

func (s *Server) publicEventPayloadForDelivery(eventID int64, payload any) any {
	if payload == nil {
		return nil
	}
	if eventID <= 0 {
		return publicEventPayload(payload)
	}
	if cached, ok := s.publicPayloads.get(eventID); ok {
		return cached
	}
	sanitized := publicEventPayload(payload)
	s.publicPayloads.put(eventID, sanitized)
	return sanitized
}

func publicEventPayload(payload any) any {
	if payload == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return sanitizePublicEventValue(value)
}

func sanitizePublicEventValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed))
		for key, item := range typed {
			if publicEventForbiddenKey(key) {
				continue
			}
			next[key] = sanitizePublicEventValue(item)
		}
		return next
	case []any:
		next := make([]any, 0, len(typed))
		for _, item := range typed {
			next = append(next, sanitizePublicEventValue(item))
		}
		return next
	default:
		return value
	}
}

func publicEventForbiddenKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case
		"generation_id",
		"active_generation_id",
		"restore_id",
		"driver_id",
		"agent",
		"driver",
		"driver_state",
		"driver_private_state",
		"data_volumes",
		"resource_identity",
		"resource_identity_payload",
		"runtime_resource_identity",
		"host_path",
		"host_paths",
		"workspace",
		"workspace_path",
		"agent_home_path",
		"checkpoint_path",
		"bundle_dir_path",
		"control_dir_path",
		"control_manifest_path",
		"bridge_dir_path",
		"log_dir_path",
		"rootfs_path",
		"network_hosts_path",
		"netns_path":
		return true
	default:
		return false
	}
}
