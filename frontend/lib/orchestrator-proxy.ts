const HOP_BY_HOP_HEADERS = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "content-length",
  "host"
]);

export const DEFAULT_ORCHESTRATOR_URL = "http://127.0.0.1:8090";

export function getOrchestratorBaseUrl() {
  const configured =
    process.env.HARNESS_API_BASE_URL ??
    process.env.ORCHESTRATOR_URL ??
    DEFAULT_ORCHESTRATOR_URL;

  return configured.replace(/\/+$/, "");
}

export function buildUpstreamUrl(pathname: string, search: string) {
  const baseUrl = getOrchestratorBaseUrl();
  const upstreamUrl = new URL(pathname, `${baseUrl}/`);
  upstreamUrl.search = search;
  return upstreamUrl;
}

function copyRequestHeaders(headers: Headers, requestUrl: URL) {
  const nextHeaders = new Headers();

  headers.forEach((value, key) => {
    const lowerKey = key.toLowerCase();
    if (HOP_BY_HOP_HEADERS.has(lowerKey) || lowerKey === "origin" || lowerKey === "referer") {
      return;
    }
    nextHeaders.set(key, value);
  });

  nextHeaders.set("x-forwarded-host", requestUrl.host);
  nextHeaders.set("x-forwarded-proto", requestUrl.protocol.replace(":", ""));

  return nextHeaders;
}

function copyResponseHeaders(headers: Headers) {
  const nextHeaders = new Headers();

  headers.forEach((value, key) => {
    if (HOP_BY_HOP_HEADERS.has(key.toLowerCase())) {
      return;
    }
    nextHeaders.append(key, value);
  });

  nextHeaders.set("cache-control", "no-store");
  return nextHeaders;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/**
 * Rejects cross-origin state-changing requests.
 *
 * The app ships `frame-ancestors *` and a `SameSite=None` auth cookie so it can
 * be embedded in third-party portals. That combination means the browser WILL
 * attach the session cookie to requests initiated by any page on the internet,
 * so SameSite is no longer the CSRF defense it used to be — this check is.
 *
 * Enforced here rather than in the orchestrator because `copyRequestHeaders`
 * strips `origin`/`referer` before forwarding: this proxy is the last hop that
 * can still see who initiated the request.
 *
 * Same-origin requests from the embedded app carry `Origin: <our own host>` and
 * pass. A request with no `Origin` at all is allowed: browsers always send it on
 * cross-origin non-GET, so absence means a non-browser client (curl, the health
 * checker), which is not a CSRF vector because there is no ambient cookie to
 * abuse.
 *
 * The comparison uses X-Forwarded-Host, NOT requestUrl.host: the app is deployed
 * behind a TLS-terminating nginx, so requestUrl is the internal 127.0.0.1:9001
 * address and would never equal the public origin the browser sent — every write
 * would 403. nginx overwrites X-Forwarded-Host with $host on every proxied
 * request, so this cannot be spoofed past it; and a cross-site attacker cannot
 * set a victim browser's Host or Origin from script anyway, which is what makes
 * matching the two a valid check.
 */
export function isCrossOriginWrite(request: Request, requestUrl: URL) {
  if (SAFE_METHODS.has(request.method)) {
    return false;
  }
  const origin = request.headers.get("origin");
  if (!origin) {
    return false;
  }
  const expectedHost = request.headers.get("x-forwarded-host") ?? requestUrl.host;
  try {
    return new URL(origin).host !== expectedHost;
  } catch {
    // Unparseable Origin is not something a browser produces; treat as hostile.
    return true;
  }
}

export async function proxyRequest(request: Request, upstreamPath: string) {
  const requestUrl = new URL(request.url);
  const upstreamUrl = buildUpstreamUrl(upstreamPath, requestUrl.search);
  const headers = copyRequestHeaders(request.headers, requestUrl);

  // Buffer non-GET/HEAD bodies into memory before forwarding. Streaming the
  // request body through (body: request.body / Readable.fromWeb(...) with
  // duplex: 'half') was attempted to avoid materializing large uploads, but in
  // this runtime (node v24.15.0 + Next 16.2.6 undici) it throws
  // `fetch failed` / `expected non-null body source`, so forwarding a stream is
  // not viable here. Buffering means a ~100MB upload is fully held in the Node
  // process for the duration of the request — an accepted tradeoff for now; the
  // orchestrator enforces the real per-file size cap.
  const body =
    request.method === "GET" || request.method === "HEAD"
      ? undefined
      : new Uint8Array(await request.arrayBuffer());

  return fetch(upstreamUrl, {
    method: request.method,
    headers,
    body,
    cache: "no-store"
  });
}

export async function proxyToResponse(request: Request, upstreamPath: string) {
  if (isCrossOriginWrite(request, new URL(request.url))) {
    return Response.json(
      { error: "cross-origin request refused" },
      { status: 403, headers: { "cache-control": "no-store" } }
    );
  }

  try {
    const upstreamResponse = await proxyRequest(request, upstreamPath);

    return new Response(upstreamResponse.body, {
      status: upstreamResponse.status,
      headers: copyResponseHeaders(upstreamResponse.headers)
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : "upstream unavailable";

    return Response.json(
      { error: message, upstream: getOrchestratorBaseUrl() },
      {
        status: 503,
        headers: {
          "cache-control": "no-store"
        }
      }
    );
  }
}
