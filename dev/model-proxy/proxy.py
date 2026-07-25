#!/usr/bin/env python3
"""Local stand-in for the ECS `claude-code-proxy` model proxy.

Sits between the sandbox agent (Pi, speaking anthropic-messages) and the
upstream provider. Two jobs only:

  1. Authorize every request against the orchestrator's proxy-correlation
     Unix socket (start -> forward -> finish), so a sandbox has model access
     only during its own active turn.
  2. Forward to the upstream endpoint, injecting the real API key. The
     sandbox only ever holds a dummy key.

Upstream here is ModelArts' *native* Anthropic-protocol endpoint, so this is a
straight pass-through — no OpenAI translation needed (unlike the ECS proxy,
which targets /openai/v1).

Env:
  PORT                              listen port (default 8083)
  UPSTREAM_BASE_URL                 e.g. https://api.modelarts-maas.com/anthropic
  UPSTREAM_API_KEY                  real provider key (never leaves the host)
  UPSTREAM_MODEL                    optional model override
  HARNESS_PROXY_CORRELATION_SOCKET  orchestrator correlation socket
  HARNESS_REQUIRE_CORRELATION       "0" to log-and-allow when correlation is
                                    unavailable (dev only; default "1")
"""

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

PORT = int(os.environ.get("PORT", "8083"))
UPSTREAM_BASE_URL = os.environ.get(
    "UPSTREAM_BASE_URL", "https://api.modelarts-maas.com/anthropic"
).rstrip("/")
UPSTREAM_API_KEY = os.environ.get("UPSTREAM_API_KEY", "")
UPSTREAM_MODEL = os.environ.get("UPSTREAM_MODEL", "").strip()
CORRELATION_SOCKET = os.environ.get(
    "HARNESS_PROXY_CORRELATION_SOCKET",
    "/var/lib/harness/run/proxy-internal/proxy-correlation.sock",
)
REQUIRE_CORRELATION = os.environ.get("HARNESS_REQUIRE_CORRELATION", "1") != "0"

_counter_lock = threading.Lock()
_counter = 0


def log(msg):
    sys.stdout.write("%s %s\n" % (time.strftime("%H:%M:%S"), msg))
    sys.stdout.flush()


def next_request_id():
    global _counter
    with _counter_lock:
        _counter += 1
        return "proxy-%d-%d" % (int(time.time() * 1000), _counter)


class UnixHTTPConnection(http.client.HTTPConnection):
    """HTTP over a Unix domain socket (the correlation API)."""

    def __init__(self, socket_path, timeout=10):
        super().__init__("localhost", timeout=timeout)
        self.socket_path = socket_path

    def connect(self):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self.timeout)
        sock.connect(self.socket_path)
        self.sock = sock


def correlation_call(path, payload):
    """POST to the correlation socket. Returns (status, decoded_body)."""
    body = json.dumps(payload).encode()
    conn = UnixHTTPConnection(CORRELATION_SOCKET)
    try:
        conn.request(
            "POST", path, body=body, headers={"Content-Type": "application/json"}
        )
        resp = conn.getresponse()
        raw = resp.read()
        try:
            decoded = json.loads(raw or b"{}")
        except ValueError:
            decoded = {"raw": raw.decode("utf-8", "replace")}
        return resp.status, decoded
    finally:
        conn.close()


_upstream = urllib.parse.urlparse(UPSTREAM_BASE_URL)
_upstream_host = _upstream.hostname
_upstream_port = _upstream.port or (443 if _upstream.scheme == "https" else 80)
_upstream_prefix = _upstream.path.rstrip("/")

# Request headers we regenerate ourselves or that must not be forwarded.
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
    "x-api-key",
    "authorization",
    "accept-encoding",
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "harness-model-proxy-dev"

    def log_message(self, fmt, *args):  # quieter default access log
        pass

    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path in ("/healthz", "/health"):
            self._json(200, {"status": "ok"})
            return
        self._json(404, {"error": "not found"})

    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        length = int(self.headers.get("Content-Length") or 0)
        raw_body = self.rfile.read(length) if length else b""

        if not path.startswith("/v1/"):
            self._json(404, {"error": "not found"})
            return

        try:
            payload = json.loads(raw_body or b"{}")
        except ValueError:
            payload = {}
        model = payload.get("model") or ""
        if UPSTREAM_MODEL:
            payload["model"] = UPSTREAM_MODEL
            model = UPSTREAM_MODEL
            raw_body = json.dumps(payload).encode()
        streaming = bool(payload.get("stream"))

        source_ip = self.client_address[0]
        request_id = next_request_id()

        # 1. authorize against the live turn context
        ctx = None
        if path.endswith("/messages"):
            try:
                status, ctx = correlation_call(
                    "/internal/proxy/requests/start",
                    {
                        "sandbox_source_ip": source_ip,
                        "proxy_request_id": request_id,
                        "upstream_model": model,
                        "upstream_base_url": UPSTREAM_BASE_URL,
                    },
                )
            except OSError as exc:
                log("correlation start failed (%s): %s" % (source_ip, exc))
                if REQUIRE_CORRELATION:
                    self._json(
                        503,
                        {
                            "type": "error",
                            "error": {
                                "type": "api_error",
                                "message": "proxy correlation unavailable",
                            },
                        },
                    )
                    return
                status, ctx = 0, None
            else:
                if status != 200:
                    log(
                        "request denied ip=%s status=%s body=%s"
                        % (source_ip, status, ctx)
                    )
                    self._json(
                        503,
                        {
                            "type": "error",
                            "error": {
                                "type": "api_error",
                                "message": "proxy request not authorized: %s"
                                % (ctx.get("error_class") or ctx.get("error") or status),
                            },
                        },
                    )
                    return
                log(
                    "request started ip=%s session=%s turn=%s seq=%s model=%s stream=%s"
                    % (
                        source_ip,
                        ctx.get("session_id"),
                        ctx.get("turn_id"),
                        ctx.get("request_sequence"),
                        model,
                        streaming,
                    )
                )

        # 2. forward upstream
        started = time.time()
        http_status = None
        error_class = ""
        error_text = ""
        try:
            http_status = self._forward(path, raw_body, streaming)
        except Exception as exc:  # noqa: BLE001 - report anything as a 502
            error_class = type(exc).__name__
            error_text = str(exc)
            log("upstream error: %s: %s" % (error_class, error_text))
            try:
                self._json(
                    502,
                    {
                        "type": "error",
                        "error": {"type": "api_error", "message": error_text},
                    },
                )
            except Exception:
                pass
            http_status = 502
        finally:
            if ctx is not None:
                total_ms = int((time.time() - started) * 1000)
                try:
                    correlation_call(
                        "/internal/proxy/requests/finish",
                        {
                            "proxy_request_id": request_id,
                            "upstream_total_latency_ms": total_ms,
                            "http_status": http_status,
                            "error_class": error_class,
                            "error": error_text,
                        },
                    )
                    log(
                        "request finished id=%s status=%s %dms"
                        % (request_id, http_status, total_ms)
                    )
                except OSError as exc:
                    log("correlation finish failed: %s" % exc)

    def _forward(self, path, raw_body, streaming):
        """Stream the upstream response straight back to the client."""
        upstream_path = _upstream_prefix + path
        headers = {
            k: v
            for k, v in self.headers.items()
            if k.lower() not in _HOP_HEADERS
        }
        headers["Host"] = _upstream_host
        headers["x-api-key"] = UPSTREAM_API_KEY
        headers.setdefault("anthropic-version", "2023-06-01")
        headers["Content-Type"] = "application/json"
        headers["Content-Length"] = str(len(raw_body))
        headers["Accept-Encoding"] = "identity"

        if _upstream.scheme == "https":
            conn = http.client.HTTPSConnection(
                _upstream_host,
                _upstream_port,
                timeout=600,
                context=ssl.create_default_context(),
            )
        else:
            conn = http.client.HTTPConnection(
                _upstream_host, _upstream_port, timeout=600
            )
        try:
            conn.request("POST", upstream_path, body=raw_body, headers=headers)
            resp = conn.getresponse()
            self.send_response(resp.status)
            content_type = resp.getheader("Content-Type") or "application/json"
            self.send_header("Content-Type", content_type)
            for name in ("anthropic-version", "request-id", "anthropic-ratelimit-requests-remaining"):
                value = resp.getheader(name)
                if value:
                    self.send_header(name, value)

            is_sse = "event-stream" in content_type or streaming
            if is_sse:
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                while True:
                    chunk = resp.read(4096)
                    if not chunk:
                        break
                    self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                    self.wfile.flush()
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
            else:
                body = resp.read()
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            return resp.status
        finally:
            conn.close()


def main():
    if not UPSTREAM_API_KEY:
        log("FATAL: UPSTREAM_API_KEY is empty")
        return 1
    log(
        "listening on 0.0.0.0:%d -> %s (model override=%s, correlation=%s)"
        % (PORT, UPSTREAM_BASE_URL, UPSTREAM_MODEL or "<passthrough>", CORRELATION_SOCKET)
    )
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    server.daemon_threads = True
    server.serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
