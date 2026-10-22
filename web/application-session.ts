// A derived application session can outlive a browser's switch to another
// central account. Never display or mutate the old account's workspace in that case.
export async function centralSessionMatches(subject: string, signal?: AbortSignal): Promise<boolean> {
  const response = await fetch("/api/auth/session", { credentials: "same-origin", cache: "no-store", signal });
  if (response.status === 401) return false;
  if (!response.ok) throw new Error("Identity unavailable");
  const identity = await response.json() as { id: string; must_change_password?: boolean };
  return identity.id === subject && !identity.must_change_password;
}
