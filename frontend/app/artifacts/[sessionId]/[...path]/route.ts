import type { NextRequest } from "next/server";
import { proxyToResponse } from "@/lib/orchestrator-proxy";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{
    sessionId: string;
    path?: string[];
  }>;
};

async function handle(request: NextRequest, context: RouteContext) {
  const params = await context.params;
  const encodedPath = params.path?.map((segment) => encodeURIComponent(segment)).join("/");
  const upstreamPath = encodedPath
    ? `/artifacts/${encodeURIComponent(params.sessionId)}/${encodedPath}`
    : `/artifacts/${encodeURIComponent(params.sessionId)}`;

  return proxyToResponse(request, upstreamPath);
}

// App Router route handlers do NOT apply the legacy `api/` 4MB body-size cap
// (that only affected the Pages Router `api/` routes via bodyParser config).
// Bodies are streamed through proxyRequest (see lib/orchestrator-proxy.ts), so
// there is no size limit to raise here; the orchestrator enforces the real cap.
export const GET = handle;
export const PUT = handle;
