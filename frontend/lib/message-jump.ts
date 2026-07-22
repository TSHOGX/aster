// Lightweight cross-component signal for "jump to a specific message".
//
// The search palette lives as a sibling of <Conversation/> under Shell, and
// Conversation reads its session from context (not props), so there is no clean
// prop path to hand it a scroll target. Rather than bloat HarnessProvider, we
// use a tiny module-level pub/sub: the palette calls `requestMessageJump(...)`
// after opening a session, and Conversation subscribes and scrolls once the
// target message has rendered.

export type MessageJumpTarget = { sessionId: string; messageId: number };

let current: MessageJumpTarget | null = null;
const listeners = new Set<() => void>();

export function requestMessageJump(sessionId: string, messageId: number) {
  current = { sessionId, messageId };
  for (const listener of listeners) listener();
}

export function clearMessageJump() {
  if (current === null) return;
  current = null;
  for (const listener of listeners) listener();
}

export function getMessageJump(): MessageJumpTarget | null {
  return current;
}

export function subscribeMessageJump(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
