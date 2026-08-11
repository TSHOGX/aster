#!/usr/bin/env python3
"""Host-side MCP gateway.

Sits between the sandbox pi-mcp client and a real upstream MCP server so the
sandbox never holds the upstream token.

Supports:
  - Legacy SSE transport (GET …/sse + POST message endpoint, endpoint rewrite)
  - Streamable HTTP transport (POST/GET/DELETE on a single MCP endpoint)

Env (see .env.example):
  PORT, BIND, PUBLIC_BASE_URL
  UPSTREAM_SSE_URL             legacy SSE URL (optional if HTTP set)
  UPSTREAM_HTTP_URL            streamable-http MCP endpoint (optional if SSE set)
  UPSTREAM_AUTHORIZATION       Authorization header value
  UPSTREAM_X_AUTH_TOKEN        X-Auth-Token header value
  REQUIRE_AUTHZ                "1" to authorize tools/call via dataplane
  HARNESS_PROXY_CORRELATION_SOCKET
"""

from __future__ import annotations

import http.client
import json
import os
import socket
import ssl
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Optional

PORT = int(os.environ.get("PORT", "8103"))
BIND = os.environ.get("BIND", "0.0.0.0")
PUBLIC_BASE_URL = os.environ.get(
    "PUBLIC_BASE_URL", f"http://127.0.0.1:{PORT}"
).rstrip("/")
UPSTREAM_SSE_URL = os.environ.get("UPSTREAM_SSE_URL", "").strip()
UPSTREAM_HTTP_URL = os.environ.get("UPSTREAM_HTTP_URL", "").strip()
UPSTREAM_AUTHORIZATION = os.environ.get("UPSTREAM_AUTHORIZATION", "").strip()
UPSTREAM_X_AUTH_TOKEN = os.environ.get("UPSTREAM_X_AUTH_TOKEN", "").strip()
REQUIRE_AUTHZ = os.environ.get("REQUIRE_AUTHZ", "0") != "0"
CORRELATION_SOCKET = os.environ.get(
    "HARNESS_PROXY_CORRELATION_SOCKET",
    "/var/lib/harness/run/proxy-internal/proxy-correlation.sock",
)

# Drop hop-by-hop + any client-supplied auth. The gateway is the only place
# that may speak the real credential.
_HOP_HEADERS = {
    "host",
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "content-length",
    "accept-encoding",
    "authorization",
    "x-auth-token",
    "x-api-key",
}

# Response headers to pass through for streamable-http (session + content).
_PASSTHROUGH_RESPONSE_HEADERS = {
    "content-type",
    "cache-control",
    "mcp-session-id",
    "mcp-protocol-version",
    "last-event-id",
}

_lock = threading.Lock()
_counter = 0


def log(msg: str) -> None:
    sys.stdout.write("%s %s\n" % (time.strftime("%H:%M:%S"), msg))
    sys.stdout.flush()


def next_id(prefix: str = "mcp") -> str:
    global _counter
    with _lock:
        _counter += 1
        return "%s-%d-%d" % (prefix, int(time.time() * 1000), _counter)


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, socket_path: str, timeout: float = 10):
        super().__init__("localhost", timeout=timeout)
        self.socket_path = socket_path

    def connect(self) -> None:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self.timeout)
        sock.connect(self.socket_path)
        self.sock = sock


def authorize_dataplane(source_ip: str) -> tuple[bool, dict]:
    """Ask orchestrator whether this sandbox IP has an active turn."""
    body = json.dumps(
        {
            "domain": "mcp",
            "sandbox_source_ip": source_ip,
        }
    ).encode()
    conn = UnixHTTPConnection(CORRELATION_SOCKET)
    try:
        conn.request(
            "POST",
            "/internal/dataplane/requests/authorize",
            body=body,
            headers={"Content-Type": "application/json"},
        )
        resp = conn.getresponse()
        raw = resp.read()
        try:
            decoded = json.loads(raw or b"{}")
        except ValueError:
            decoded = {"raw": raw.decode("utf-8", "replace")}
        ok = resp.status == 200 and bool(decoded.get("authorized"))
        return ok, decoded
    finally:
        conn.close()


def open_upstream(url: str, method: str, headers: dict, body: bytes | None = None):
    parsed = urllib.parse.urlparse(url)
    host = parsed.hostname
    if not host:
        raise ValueError("upstream url missing host: %r" % url)
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    path = parsed.path or "/"
    if parsed.query:
        path = path + "?" + parsed.query
    if parsed.scheme == "https":
        conn = http.client.HTTPSConnection(
            host, port, timeout=600, context=ssl.create_default_context()
        )
    else:
        conn = http.client.HTTPConnection(host, port, timeout=600)
    req_headers = dict(headers)
    req_headers["Host"] = host if not parsed.port else "%s:%d" % (host, port)
    conn.request(method, path, body=body, headers=req_headers)
    return conn, conn.getresponse()


def inject_upstream_auth(headers: dict) -> dict:
    out = dict(headers)
    if UPSTREAM_AUTHORIZATION:
        out["Authorization"] = UPSTREAM_AUTHORIZATION
    if UPSTREAM_X_AUTH_TOKEN:
        out["X-Auth-Token"] = UPSTREAM_X_AUTH_TOKEN
    return out


def filter_request_headers(raw_headers) -> dict:
    return {
        k: v
        for k, v in raw_headers.items()
        if k.lower() not in _HOP_HEADERS
    }


def rewrite_endpoint_data(data: str, public_base: str) -> str:
    """Rewrite an SSE endpoint event payload to this gateway's origin."""
    data = data.strip()
    if not data:
        return data
    public = urllib.parse.urlparse(public_base)
    if data.startswith("http://") or data.startswith("https://"):
        upstream = urllib.parse.urlparse(data)
        return urllib.parse.urlunparse(
            (
                public.scheme,
                public.netloc,
                upstream.path,
                "",
                upstream.query,
                "",
            )
        )
    return data


def rewrite_sse_chunk(chunk: bytes, public_base: str) -> bytes:
    """Rewrite endpoint events inside an SSE byte stream (best-effort)."""
    try:
        text = chunk.decode("utf-8")
    except UnicodeDecodeError:
        return chunk

    lines = text.splitlines(keepends=True)
    out: list[str] = []
    pending_event: Optional[str] = None
    for line in lines:
        if line.startswith("event:"):
            pending_event = line[len("event:") :].strip()
            out.append(line)
            continue
        if line.startswith("data:") and pending_event == "endpoint":
            raw = line[len("data:") :]
            leading = ""
            body = raw
            if body.startswith(" "):
                leading = " "
                body = body[1:]
            nl = ""
            if body.endswith("\r\n"):
                nl = "\r\n"
                body = body[:-2]
            elif body.endswith("\n"):
                nl = "\n"
                body = body[:-1]
            rewritten = rewrite_endpoint_data(body, public_base)
            out.append("data:" + leading + rewritten + nl)
            continue
        if line in ("\n", "\r\n"):
            pending_event = None
        out.append(line)
    return "".join(out).encode("utf-8")


def is_tools_call(body: bytes) -> bool:
    try:
        payload = json.loads(body or b"{}")
    except ValueError:
        return False
    if isinstance(payload, list):
        return any(
            isinstance(item, dict) and item.get("method") == "tools/call"
            for item in payload
        )
    return payload.get("method") == "tools/call"


def is_legacy_sse_path(path: str) -> bool:
    return path == "/sse" or path.endswith("/sse")


def streamable_http_target(query: str) -> str:
    """Map any client path to the configured streamable-http endpoint."""
    if not UPSTREAM_HTTP_URL:
        raise ValueError("UPSTREAM_HTTP_URL not configured")
    if not query:
        return UPSTREAM_HTTP_URL
    sep = "&" if ("?" in UPSTREAM_HTTP_URL) else "?"
    return UPSTREAM_HTTP_URL + sep + query


def _safe_id(body: bytes):
    try:
        payload = json.loads(body or b"{}")
    except ValueError:
        return None
    if isinstance(payload, list) and payload:
        first = payload[0]
        return first.get("id") if isinstance(first, dict) else None
    if isinstance(payload, dict):
        return payload.get("id")
    return None


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "harness-mcp-gateway"

    def log_message(self, fmt, *args):
        pass

    def _json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _jsonrpc_error(self, req_id, code: int, message: str, http_status: int = 200) -> None:
        body = json.dumps(
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": code, "message": message},
            }
        ).encode()
        self.send_response(http_status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def headers_sent_safe(self) -> bool:
        return getattr(self, "_headers_sent_flag", False)

    def send_response(self, code, message=None):
        self._headers_sent_flag = True
        return super().send_response(code, message)

    def _maybe_authz_tools_call(self, raw_body: bytes) -> bool:
        """Return False if the request was already answered (denied/error)."""
        if not (REQUIRE_AUTHZ and is_tools_call(raw_body)):
            return True
        source_ip = self.client_address[0]
        try:
            ok, ctx = authorize_dataplane(source_ip)
        except OSError as exc:
            log("authz unavailable ip=%s err=%s" % (source_ip, exc))
            self._jsonrpc_error(
                _safe_id(raw_body),
                -32000,
                "mcp gateway authz unavailable",
            )
            return False
        if not ok:
            reason = ctx.get("reason") or ctx.get("error") or "denied"
            log("tools/call denied ip=%s reason=%s" % (source_ip, reason))
            self._jsonrpc_error(
                _safe_id(raw_body),
                -32001,
                "mcp tools/call not authorized: %s" % reason,
            )
            return False
        log(
            "tools/call authorized ip=%s session=%s turn=%s"
            % (source_ip, ctx.get("session_id"), ctx.get("turn_id"))
        )
        return True

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path in ("/healthz", "/health"):
            self._json(
                200,
                {
                    "status": "ok",
                    "upstream_sse": UPSTREAM_SSE_URL or None,
                    "upstream_http": UPSTREAM_HTTP_URL or None,
                    "require_authz": REQUIRE_AUTHZ,
                },
            )
            return

        if is_legacy_sse_path(path):
            if not UPSTREAM_SSE_URL:
                self._json(404, {"error": "UPSTREAM_SSE_URL not configured"})
                return
            self._proxy_sse()
            return

        if UPSTREAM_HTTP_URL:
            self._proxy_streamable("GET", parsed.query, b"")
            return

        self._json(404, {"error": "not found", "path": path})

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        length = int(self.headers.get("Content-Length") or 0)
        raw_body = self.rfile.read(length) if length else b""

        if not self._maybe_authz_tools_call(raw_body):
            return

        # When both transports are configured:
        #   - POSTs that look like legacy SSE message channels keep SSE forwarding
        #   - everything else uses streamable-http
        if UPSTREAM_HTTP_URL and (
            not UPSTREAM_SSE_URL or not self._looks_like_legacy_sse_post(parsed)
        ):
            self._proxy_streamable("POST", parsed.query, raw_body)
            return

        if UPSTREAM_SSE_URL:
            self._proxy_sse_post(parsed.path, parsed.query, raw_body)
            return

        self._json(500, {"error": "no upstream configured"})

    def do_DELETE(self) -> None:
        # Streamable HTTP session teardown (Mcp-Session-Id).
        parsed = urllib.parse.urlparse(self.path)
        if not UPSTREAM_HTTP_URL:
            self._json(404, {"error": "UPSTREAM_HTTP_URL not configured"})
            return
        self._proxy_streamable("DELETE", parsed.query, b"")

    def _looks_like_legacy_sse_post(self, parsed: urllib.parse.ParseResult) -> bool:
        """Heuristic: legacy SSE clients POST to the endpoint from the SSE event."""
        path = parsed.path
        q = urllib.parse.parse_qs(parsed.query)
        if "sessionId" in q or "session_id" in q:
            return True
        if path.endswith("/mcp") or path.endswith("/message"):
            return True
        if "/api/v1/mcp" in path:
            return True
        return False

    def _proxy_sse(self) -> None:
        req_id = next_id("sse")
        source_ip = self.client_address[0]
        log("SSE open id=%s ip=%s -> %s" % (req_id, source_ip, UPSTREAM_SSE_URL))

        headers = inject_upstream_auth(filter_request_headers(self.headers))
        headers["Accept"] = "text/event-stream"
        headers["Accept-Encoding"] = "identity"
        headers["Connection"] = "keep-alive"

        conn = None
        try:
            conn, resp = open_upstream(UPSTREAM_SSE_URL, "GET", headers)
            content_type = resp.getheader("Content-Type") or "text/event-stream"
            self.send_response(resp.status)
            self.send_header("Content-Type", content_type)
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self._stream_body(resp, rewrite_endpoints=True)
            log("SSE closed id=%s" % req_id)
        except Exception as exc:  # noqa: BLE001
            log("SSE error id=%s: %s: %s" % (req_id, type(exc).__name__, exc))
            try:
                if not self.headers_sent_safe():
                    self._json(502, {"error": str(exc)})
            except Exception:
                pass
        finally:
            if conn is not None:
                conn.close()

    def _proxy_sse_post(self, path: str, query: str, raw_body: bytes) -> None:
        upstream = urllib.parse.urlparse(UPSTREAM_SSE_URL)
        target = urllib.parse.urlunparse(
            (upstream.scheme, upstream.netloc, path, "", query, "")
        )
        self._forward_once("POST", target, raw_body, stream_sse=True)

    def _proxy_streamable(self, method: str, query: str, raw_body: bytes) -> None:
        try:
            target = streamable_http_target(query)
        except ValueError as exc:
            self._json(500, {"error": str(exc)})
            return
        self._forward_once(method, target, raw_body, stream_sse=True)

    def _forward_once(
        self,
        method: str,
        target: str,
        raw_body: bytes,
        stream_sse: bool,
    ) -> None:
        req_id = next_id(method.lower())
        source_ip = self.client_address[0]
        method_name = method
        if method == "POST" and raw_body:
            try:
                payload = json.loads(raw_body)
                if isinstance(payload, dict):
                    method_name = payload.get("method") or method
                elif isinstance(payload, list) and payload and isinstance(payload[0], dict):
                    method_name = payload[0].get("method") or method
            except ValueError:
                pass
        log(
            "%s id=%s ip=%s method=%s -> %s"
            % (method, req_id, source_ip, method_name, target)
        )

        headers = inject_upstream_auth(filter_request_headers(self.headers))
        headers["Accept-Encoding"] = "identity"
        if method in ("POST", "PUT", "PATCH"):
            headers["Content-Type"] = (
                self.headers.get("Content-Type") or "application/json"
            )
            headers["Content-Length"] = str(len(raw_body))
            headers.setdefault(
                "Accept",
                self.headers.get("Accept") or "application/json, text/event-stream",
            )
        elif "Accept" not in {k.title(): k for k in headers}:
            # Preserve client Accept when present (already in filtered headers).
            if not any(k.lower() == "accept" for k in headers):
                headers["Accept"] = self.headers.get("Accept") or (
                    "text/event-stream, application/json"
                )

        conn = None
        try:
            conn, resp = open_upstream(
                target, method, headers, raw_body if method != "GET" else None
            )
            content_type = (resp.getheader("Content-Type") or "").lower()
            is_sse = stream_sse and "text/event-stream" in content_type

            self.send_response(resp.status)
            for name, value in resp.getheaders():
                if name.lower() in _PASSTHROUGH_RESPONSE_HEADERS:
                    self.send_header(name, value)
            if is_sse:
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                if not any(h[0].lower() == "content-type" for h in resp.getheaders()):
                    self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self._stream_body(resp, rewrite_endpoints=False)
            else:
                body = resp.read()
                if not any(h[0].lower() == "content-type" for h in resp.getheaders()):
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            log(
                "%s done id=%s status=%s sse=%s"
                % (method, req_id, resp.status, is_sse)
            )
        except Exception as exc:  # noqa: BLE001
            log("%s error id=%s: %s: %s" % (method, req_id, type(exc).__name__, exc))
            try:
                if not self.headers_sent_safe():
                    self._json(502, {"error": str(exc)})
            except Exception:
                pass
        finally:
            if conn is not None:
                conn.close()

    def _stream_body(self, resp, rewrite_endpoints: bool) -> None:
        # IMPORTANT: use fp.read1(), not resp.read().
        # BufferedReader.read(n) blocks filling its buffer on long-lived streams.
        while True:
            try:
                chunk = resp.fp.read1(4096)
            except http.client.IncompleteRead as exc:
                chunk = exc.partial or b""
                if chunk:
                    if rewrite_endpoints:
                        chunk = rewrite_sse_chunk(chunk, PUBLIC_BASE_URL)
                    self.wfile.write(chunk)
                    self.wfile.flush()
                break
            except (socket.timeout, TimeoutError):
                continue
            if not chunk:
                break
            if rewrite_endpoints:
                chunk = rewrite_sse_chunk(chunk, PUBLIC_BASE_URL)
            self.wfile.write(chunk)
            self.wfile.flush()


def main() -> int:
    if not UPSTREAM_SSE_URL and not UPSTREAM_HTTP_URL:
        log("FATAL: set UPSTREAM_SSE_URL and/or UPSTREAM_HTTP_URL")
        return 1
    log(
        "listening on %s:%d public=%s sse=%s http=%s authz=%s"
        % (
            BIND,
            PORT,
            PUBLIC_BASE_URL,
            UPSTREAM_SSE_URL or "<off>",
            UPSTREAM_HTTP_URL or "<off>",
            REQUIRE_AUTHZ,
        )
    )
    if UPSTREAM_AUTHORIZATION:
        log("upstream Authorization: set (%d chars)" % len(UPSTREAM_AUTHORIZATION))
    else:
        log("upstream Authorization: <empty>")
    if UPSTREAM_X_AUTH_TOKEN:
        log("upstream X-Auth-Token: set (%d chars)" % len(UPSTREAM_X_AUTH_TOKEN))
    server = ThreadingHTTPServer((BIND, PORT), Handler)
    server.daemon_threads = True
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log("shutting down")
    return 0


if __name__ == "__main__":
    sys.exit(main())
