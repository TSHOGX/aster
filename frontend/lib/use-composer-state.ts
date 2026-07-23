import { useState, type KeyboardEvent } from "react";

type SendResult = { ok: boolean; error?: string };

// Shared message-composer state: the input value, the in-flight/error flags, and
// the submit + Enter-to-send handlers common to the agent Composer and the shell
// ShellComposer. The auto-grow textarea effects and the shell's interrupt flow
// stay in the components since they differ; setError is exposed so the shell can
// surface interrupt failures through the same error line.
export function useComposerState(
  onSend: (content: string) => Promise<SendResult>,
  disabled?: boolean
) {
  const [value, setValue] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    const text = value.trim();
    if (!text || sending || disabled) return;
    setSending(true);
    setError(null);
    const res = await onSend(text);
    setSending(false);
    if (res.ok) {
      setValue("");
    } else {
      setError(res.error ?? "Failed to send");
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void submit();
    }
  };

  return { value, setValue, sending, error, setError, submit, onKey };
}
