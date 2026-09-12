import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { SSE_TYPED_EVENT_TYPES } from "./use-event-stream";

/**
 * The SSE registration whitelist is an implicit contract with two other places,
 * and nothing but these tests holds it together.
 *
 * The server frames every event as `event: <type>` (writeSSEEventFramed), and
 * EventSource delivers a NAMED frame only to a listener registered for that
 * exact name — `onmessage` never sees it. So a type missing from
 * SSE_TYPED_EVENT_TYPES is not a degraded path: it is total, silent loss, with a
 * handler that looks correct sitting right there in the provider.
 *
 * That is how `session.runtime` was lost. The provider had a `case` for it and
 * the server pushed it from six places; every frame was dropped, so the queue
 * only ever updated on a follow-up GET — which let a dispatched turn linger in
 * the queue and then execute a second time.
 */
describe("SSE_TYPED_EVENT_TYPES", () => {
  it("registers the runtime projection", () => {
    // Pinned explicitly, not just via the drift check below: this is the frame
    // that carries queue state, and losing it silently reintroduces duplicate
    // turn execution.
    expect(SSE_TYPED_EVENT_TYPES).toContain("session.runtime");
  });

  it("has no duplicate entries", () => {
    expect([...new Set(SSE_TYPED_EVENT_TYPES)]).toHaveLength(SSE_TYPED_EVENT_TYPES.length);
  });

  /**
   * The real invariant: anything the provider bothers to handle must be able to
   * arrive. Reading the source keeps this honest without a DOM/React harness —
   * the provider's switch is the list of events the client claims to support, so
   * comparing the two catches the next omission generically rather than pinning
   * one known name.
   */
  it("registers every event type the provider handles", () => {
    const provider = readFileSync(
      join(__dirname, "..", "components", "harness-provider.tsx"),
      "utf8"
    );
    // Event types are dotted or snake_cased identifiers; the provider also
    // switches on inner pi status types (`turn_start`, …) that are payload
    // discriminants rather than SSE frame names, so require a dot or a known
    // bridge frame name to avoid matching those.
    const bridgeFrameNames = new Set(["ack_turn_started", "emit_output", "ack_turn_completed"]);
    const handled = new Set(
      [...provider.matchAll(/case "([a-z_]+(?:\.[a-z_]+)+|[a-z_]+)":/g)]
        .map((match) => match[1])
        .filter((type) => type.includes(".") || bridgeFrameNames.has(type))
    );
    // `replay_gap` and `error` are consumed inside use-event-stream itself
    // rather than by the provider, so they are registered without a case there.
    const registered = new Set<string>(SSE_TYPED_EVENT_TYPES);
    const missing = [...handled].filter((type) => !registered.has(type)).sort();
    expect(missing).toEqual([]);
  });
});
