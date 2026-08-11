#!/usr/bin/env python3
"""Host-side MCP gateway (MVP).

Sits between the sandbox pi-mcp client and a real/upstream MCP server so the
sandbox never holds the upstream token.

MVP scope (intentional):
  - Legacy SSE transport only (GET /sse + POST message endpoint)
  - Single upstream
  - Inject Authorization / X-Auth-Token from EnvironmentFile
  - Rewrite SSE `endpoint` events so the client posts back to THIS gateway
  - Optional per-tools/call authz against orchestrator dataplane authorize
    (off by default for local bring-up)

Env (see .env.example):
  PORT                         listen port (default 8103)
  BIND                         listen address (default 0.0.0.0)
  PUBLIC_BASE_URL              base URL the sandbox uses to reach us
                               (default http://127.0.0.1:$PORT)
  UPSTREAM_SSE_URL             full upstream SSE URL (required)
  UPSTREAM_AUTHORIZATION       value for Authorization header (optional)
  UPSTREAM_X_AUTH_TOKEN        value for X-Auth-Token header (optional)
  REQUIRE_AUTHZ                "1" to authorize tools/call via dataplane
  HARNESS_PROXY_CORRELATION_SOCKET
                               orchestrator unix socket for authz
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
    """Rewrite an SSE endpoint event payload to this gateway's origin.

    MCP SSE servers emit either a path (`/api/v1/mcp?sessionId=...`) or an
    absolute URL. Absolute upstream URLs must become gateway URLs or the
    sandbox will bypass us (and lose injected credentials).
    """
    data = data.strip()
    if not data:
        return data
    public = urllib.parse.urlparse(public_base)
    if data.startswith("http://") or data.startswith("https://"):
        upstream = urllib.parse.urlparse(data)
        # Keep path + query; force gateway scheme/host/port.
        rewritten = urllib.parse.urlunparse(
            (
                public.scheme,
                public.netloc,
                upstream.path,
                "",
                upstream.query,
                "",
            )
        )
        return rewritten
    # Relative path — leave as-is; the MCP client resolves against the SSE URL
    # origin, which is already the gateway.
    return data


def rewrite_sse_chunk(chunk: bytes, public_base: str) -> bytes:
    """Rewrite endpoint events inside an SSE byte stream (best-effort).

    Processes complete lines. Incomplete trailing lines are passed through and
    may miss a rewrite on a pathological split; for typical small endpoint
    events this is fine.
    """
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
            # Preserve leading space if present after the colon.
            leading = ""
            body = raw
            if body.startswith(" "):
                leading = " "
                body = body[1:]
            # Strip only the trailing newline for rewrite, then put it back.
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
    return payload.get("method") == "tools/call"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "harness-mcp-gateway-mvp"

    def log_message(self, fmt, *args):  # quieter default access log
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

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path in ("/healthz", "/health"):
            self._json(
                200,
                {
                    "status": "ok",
                    "upstream_sse": UPSTREAM_SSE_URL or None,
                    "require_authz": REQUIRE_AUTHZ,
                },
            )
            return
        # Accept both a bare /sse and a path-preserving mirror of the upstream.
        if path.endswith("/sse") or path == "/sse":
            self._proxy_sse()
            return
        self._json(404, {"error": "not found", "path": path})

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        length = int(self.headers.get("Content-Length") or 0)
        raw_body = self.rfile.read(length) if length else b""

        # Authz only on tools/call (see design review): SSE connect + tools/list
        # must work outside a turn so pi-mcp can register tools at session_start.
        if REQUIRE_AUTHZ and is_tools_call(raw_body):
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
                return
            if not ok:
                reason = ctx.get("reason") or ctx.get("error") or "denied"
                log("tools/call denied ip=%s reason=%s" % (source_ip, reason))
                self._jsonrpc_error(
                    _safe_id(raw_body),
                    -32001,
                    "mcp tools/call not authorized: %s" % reason,
                )
                return
            log(
                "tools/call authorized ip=%s session=%s turn=%s"
                % (source_ip, ctx.get("session_id"), ctx.get("turn_id"))
            )

        self._proxy_post(path, parsed.query, raw_body)

    def _proxy_sse(self) -> None:
        if not UPSTREAM_SSE_URL:
            self._json(500, {"error": "UPSTREAM_SSE_URL not configured"})
            return
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

            # IMPORTANT: use fp.read1(), not resp.read().
            # BufferedReader.read(n) tries to refill its buffer from the socket
            # and blocks until the buffer is full or the peer closes — fatal for
            # long-lived SSE. read1() returns whatever is already available.
            while True:
                try:
                    chunk = resp.fp.read1(4096)
                except http.client.IncompleteRead as exc:
                    chunk = exc.partial or b""
                    if chunk:
                        rewritten = rewrite_sse_chunk(chunk, PUBLIC_BASE_URL)
                        self.wfile.write(rewritten)
                        self.wfile.flush()
                    break
                except (socket.timeout, TimeoutError):
                    # Idle SSE; keep waiting for the next event/heartbeat.
                    continue
                if not chunk:
                    break
                rewritten = rewrite_sse_chunk(chunk, PUBLIC_BASE_URL)
                self.wfile.write(rewritten)
                self.wfile.flush()
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

    def headers_sent_safe(self) -> bool:
        # BaseHTTPRequestHandler has no public flag across all Pythons; probe
        # wfile by checking whether we've already written a status line via our
        # own attribute.
        return getattr(self, "_headers_sent_flag", False)

    def send_response(self, code, message=None):
        self._headers_sent_flag = True
        return super().send_response(code, message)

    def _proxy_post(self, path: str, query: str, raw_body: bytes) -> None:
        if not UPSTREAM_SSE_URL:
            self._json(500, {"error": "UPSTREAM_SSE_URL not configured"})
            return

        # Derive the upstream origin from the SSE URL, then append the request
        # path/query the client used against us. This keeps endpoint-event
        # relative paths working after rewrite.
        upstream = urllib.parse.urlparse(UPSTREAM_SSE_URL)
        upstream_path = path
        # If the client hit our /sse-mirrored path layout, keep it. If they hit
        # a short /mcp path from a rewritten absolute endpoint, also fine.
        target = urllib.parse.urlunparse(
            (
                upstream.scheme,
                upstream.netloc,
                upstream_path,
                "",
                query,
                "",
            )
        )

        req_id = next_id("post")
        source_ip = self.client_address[0]
        method_name = "?"
        try:
            method_name = json.loads(raw_body or b"{}").get("method") or "?"
        except ValueError:
            pass
        log(
            "POST id=%s ip=%s method=%s -> %s"
            % (req_id, source_ip, method_name, target)
        )

        headers = inject_upstream_auth(filter_request_headers(self.headers))
        headers["Content-Type"] = self.headers.get("Content-Type") or "application/json"
        headers["Accept"] = self.headers.get("Accept") or "application/json, text/event-stream"
        headers["Accept-Encoding"] = "identity"
        headers["Content-Length"] = str(len(raw_body))

        conn = None
        try:
            conn, resp = open_upstream(target, "POST", headers, raw_body)
            body = resp.read()
            content_type = resp.getheader("Content-Type") or "application/json"
            self.send_response(resp.status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            log("POST done id=%s status=%s bytes=%d" % (req_id, resp.status, len(body)))
        except Exception as exc:  # noqa: BLE001
            log("POST error id=%s: %s: %s" % (req_id, type(exc).__name__, exc))
            try:
                self._json(502, {"error": str(exc)})
            except Exception:
                pass
        finally:
            if conn is not None:
                conn.close()


def _safe_id(body: bytes):
    try:
        return json.loads(body or b"{}").get("id")
    except ValueError:
        return None


def main() -> int:
    if not UPSTREAM_SSE_URL:
        log("FATAL: UPSTREAM_SSE_URL is empty")
        return 1
    log(
        "listening on %s:%d public=%s upstream=%s authz=%s"
        % (BIND, PORT, PUBLIC_BASE_URL, UPSTREAM_SSE_URL, REQUIRE_AUTHZ)
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
