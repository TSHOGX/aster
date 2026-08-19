import type { ApiMessage } from "./types";

// Whether an assistant message is the final reply of its turn — the answer to
// the user's message, as opposed to an intermediate tool-calling step. That is
// where the Message Action Bar (thumbs / copy) belongs.
//
// We trust pi's stop_reason when present ("stop" = final reply, "toolUse" =
// interstitial step). When it is absent — salvaged text, or history written
// before stop_reason was persisted — we fall back to structure: the message is
// final if nothing follows it, or the next message is the user's next prompt.
export function isFinalReply(message: ApiMessage, next: ApiMessage | undefined): boolean {
  if (message.role !== "assistant") return false;
  if (message.stop_reason === "stop") return true;
  if (message.stop_reason === "toolUse") return false;
  // A failed model call ended the turn without answering. It is the last message
  // of its turn, but it is not a reply — offering thumbs/copy on an error notice
  // would be nonsense, and `wasInterrupted` handles it instead.
  if (message.stop_reason === "error") return false;
  return next === undefined || next.role === "user";
}

// Whether the conversation's last turn ended without delivering an answer, so
// the UI can offer to resume it.
//
// Two shapes qualify, both observed in production:
//   - stop_reason "error": the model call failed (rate limit, proxy loss) and we
//     recorded a notice in place of the reply.
//   - stop_reason "toolUse" as the very last message: the agent announced a tool
//     call and the turn died before the result came back, so the transcript just
//     stops mid-loop.
//
// Only meaningful once the turn is no longer running; callers must gate on the
// session not being live, or a healthy in-flight turn reads as interrupted.
export function wasInterrupted(messages: ApiMessage[]): boolean {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (message.role === "user") return false;
    if (message.role !== "assistant") continue;
    if (message.stop_reason === "error") return true;
    if (message.stop_reason === "toolUse") return true;
    if (message.stop_reason === "stop") return false;
    // Unknown terminator (salvaged text, pre-migration history, shell output):
    // not enough signal to claim interruption.
    return false;
  }
  return false;
}
