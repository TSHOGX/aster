import { SharedSessionView } from "@/components/shared/shared-session-view";

export const dynamic = "force-dynamic";

/**
 * The public, read-only view of a shared session.
 *
 * This page mounts NO workbench provider. HarnessProvider owns auth, the SSE
 * connection and the session list; rendering it here would fire an authenticated
 * request as its first act and bounce an anonymous visitor to a login screen.
 * Everything below is fed by the page's own fetches against /api/shared/*, which
 * the orchestrator serves outside its auth middleware.
 */
export default async function SharedSessionPage({
  params
}: {
  params: Promise<{ token: string }>;
}) {
  const { token } = await params;
  return <SharedSessionView token={token} />;
}
