import { useEffect, useRef, type MutableRefObject } from "react";

import type { ConnectionStatus, HarnessEvent } from "./types";
import { buildEventsStreamUrl } from "./ws";

// The set of typed SSE events the server may name on a frame. EventSource only
// dispatches to `onmessage` for unnamed events, so each named type must be
// registered explicitly or those frames are silently dropped.
export const SSE_TYPED_EVENT_TYPES = [
  "session.created",
  "session.running_active",
  "session.running_idle",
  "session.checkpointing",
  "session.checkpointed",
  "session.failed",
  "session.destroyed",
  "session.updated",
  // The runtime projection (queue + active turn). Named frames reach the client
  // ONLY via addEventListener, so omitting a type here drops every push of it
  // silently — which is exactly how this one was lost: the provider's handler
  // existed but was unreachable, leaving the queue to update only on a
  // follow-up GET, so a dispatched turn stayed visible in the queue and ran a
  // second time.
  "session.runtime",
  "message.created",
  "agent.message",
  "agent.delta",
  "agent.output",
  "system.status",
  "session.error",
  "generation.error",
  "session.checkpoint_retired",
  "session.restore_fallback_retired",
  "artifact.updated",
  "artifact.deleted",
  "ack_turn_started",
  "emit_output",
  "ack_turn_completed",
  "proxy.request.started",
  "proxy.request.completed",
  "proxy.request.failed",
  "replay_gap",
  "error",
  "notification.created"
] as const;

type EventStreamOptions = {
  // Gate connection on auth readiness; when false the stream stays closed.
  enabled: boolean;
  // Shared with the provider: flipped false on teardown so in-flight polls stop.
  aliveRef: MutableRefObject<boolean>;
  // Shared with the provider: the last event id seen, replayed on reconnect and
  // reset on login. Updated here as frames arrive.
  lastEventIdRef: MutableRefObject<number | null>;
  // Domain event handler. Kept in a ref internally so a new handler identity
  // (its deps change every render) never tears down and rebuilds the stream.
  onEvent: (event: HarnessEvent) => void;
  // Full reload — invoked on boot, on reconnect, and on a replay_gap frame.
  onRefresh: () => void;
  // Push a new connection status into the store.
  onConnectionChange: (status: ConnectionStatus) => void;
};

// Owns the EventSource lifecycle: open, typed-event registration, native
// reconnect handling (connecting → live → reconnecting/down), lastEventId
// tracking, and replay-gap recovery. Extracted verbatim from HarnessProvider.
export function useEventStream({
  enabled,
  aliveRef,
  lastEventIdRef,
  onEvent,
  onRefresh,
  onConnectionChange
}: EventStreamOptions) {
  // Keep the latest onEvent without making it an effect dependency: the handler
  // closes over changing state each render, but the subscription must persist.
  const onEventRef = useRef(onEvent);
  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    if (!enabled) return;
    aliveRef.current = true;
    let cleanedUp = false;
    let connectedOnce = false;

    let source: EventSource;
    try {
      source = new EventSource(buildEventsStreamUrl(lastEventIdRef.current));
    } catch {
      const failureTimer = setTimeout(() => {
        if (!cleanedUp) {
          onConnectionChange("down");
        }
      }, 0);
      return () => {
        cleanedUp = true;
        aliveRef.current = false;
        clearTimeout(failureTimer);
      };
    }

    source.onopen = () => {
      if (cleanedUp) return;
      connectedOnce = true;
      onConnectionChange("live");
      // Also reconcile the first connection: boot HTTP requests can finish
      // before the server attaches this subscription, leaving a delivery gap.
      onRefresh();
    };

    const handleSourceEvent = (ev: MessageEvent) => {
      try {
        const data = JSON.parse(typeof ev.data === "string" ? ev.data : "");
        const eventID =
          data && typeof data.event_id === "number"
            ? data.event_id
            : Number.parseInt(ev.lastEventId, 10);
        if (Number.isFinite(eventID) && eventID > 0) {
          lastEventIdRef.current = eventID;
        }
        if (data && data.type === "replay_gap") {
          onRefresh();
          return;
        }
        if (data && typeof data.type === "string") {
          onEventRef.current(data as HarnessEvent);
        }
      } catch {
        // ignore malformed frames
      }
    };
    source.onmessage = handleSourceEvent;
    for (const eventType of SSE_TYPED_EVENT_TYPES) {
      source.addEventListener(eventType, handleSourceEvent);
    }

    source.onerror = () => {
      if (cleanedUp) return;
      if (source.readyState === EventSource.CLOSED) {
        onConnectionChange("down");
        return;
      }
      onConnectionChange(connectedOnce ? "reconnecting" : "connecting");
    };

    const bootTimer = setTimeout(() => {
      onRefresh();
    }, 0);

    return () => {
      cleanedUp = true;
      aliveRef.current = false;
      clearTimeout(bootTimer);
      for (const eventType of SSE_TYPED_EVENT_TYPES) {
        source.removeEventListener(eventType, handleSourceEvent);
      }
      source.close();
    };
  }, [enabled, aliveRef, lastEventIdRef, onRefresh, onConnectionChange]);
}
