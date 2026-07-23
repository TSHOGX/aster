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
  return next === undefined || next.role === "user";
}
