// Copying text, with a fallback for insecure contexts.
//
// `navigator.clipboard` is gated behind a secure context, so it is simply absent
// when the workbench is reached over plain http — which is how local dev is
// reached at a VM's LAN address (localhost would be treated as secure, a LAN IP
// is not). The deployed workbench is behind TLS and always takes the first path.
//
// The fallback is the pre-Clipboard-API technique: put the text in an offscreen
// textarea, select it, and let the synchronous `document.execCommand("copy")`
// read the selection. It is deprecated but still implemented everywhere, and it
// must be called from within a user-gesture task, which it is (a click handler).
export async function copyTextToClipboard(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied or a transient failure; fall through and try the
      // legacy path rather than giving up outright.
    }
  }
  return copyViaExecCommand(text);
}

function copyViaExecCommand(text: string): boolean {
  if (typeof document === "undefined") return false;

  const textarea = document.createElement("textarea");
  textarea.value = text;
  // Keep it out of view without using display:none or visibility:hidden, which
  // would make the selection uncopyable.
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";

  const previousSelection = document.getSelection();
  const previousRange =
    previousSelection && previousSelection.rangeCount > 0 ? previousSelection.getRangeAt(0) : null;

  document.body.appendChild(textarea);
  try {
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
    // Restore whatever the user had selected before the copy.
    if (previousRange) {
      const selection = document.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(previousRange);
    }
  }
}
