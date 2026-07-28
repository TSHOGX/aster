import type { NextRequest } from "next/server";
import { proxyToResponse } from "@/lib/orchestrator-proxy";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{
    token: string;
    path?: string[];
  }>;
};

/**
 * Proxy for the anonymous snapshot-file surface.
 *
 * The existing handlers cover `/api/*` and `/artifacts/*` only, so this
 * top-level `/shared/...` path needs its own forwarder. It carries no
 * credentials by design: the orchestrator serves this subtree outside its auth
 * middleware and authorizes purely on the token in the path.
 *
 * GET/HEAD only. There is no write path to this surface upstream, and not
 * exporting the other verbs keeps it that way here too.
 */
async function handle(request: NextRequest, context: RouteContext) {
  const params = await context.params;
  const encodedPath = (params.path ?? []).map((segment) => encodeURIComponent(segment)).join("/");
  if (!encodedPath) {
    return new Response("not found", { status: 404 });
  }
  return proxyToResponse(
    request,
    `/shared/${encodeURIComponent(params.token)}/files/${encodedPath}`
  );
}

export const GET = handle;
export const HEAD = handle;
