"""Minimal JSON-RPC MCP stdio server for the knowledge catalog.

No extra dependencies. Speaks newline-delimited JSON-RPC 2.0 on stdin/stdout
(MCP stdio transport). Tools: `kb_catalog`, `kb_get`.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, Optional

import agents_knowledge as kb

PROTOCOL_VERSION = "2024-11-05"
SERVER_NAME = "dingpan-knowledge"
SERVER_VERSION = "0.1.0"

TOOLS = [
    {
        "name": "kb_catalog",
        "description": (
            "List available knowledge-base articles as id + description + tags. "
            "Call this first. The catalog has summaries only — do not invent "
            "article bodies. If an id's description matches the user question, "
            "call kb_get with that exact id before answering."
        ),
        "inputSchema": {
            "type": "object",
            "additionalProperties": False,
            "properties": {},
        },
    },
    {
        "name": "kb_get",
        "description": (
            "Load the full Markdown body of one knowledge-base article. "
            "Use the exact id from kb_catalog when the user question matches "
            "that article's description, then answer from the loaded body."
        ),
        "inputSchema": {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "id": {
                    "type": "string",
                    "description": "Exact knowledge article id from kb_catalog.",
                }
            },
            "required": ["id"],
        },
    },
]


def handle_rpc(message: dict[str, Any], root: Path, max_description: int) -> Optional[dict[str, Any]]:
    """Handle one JSON-RPC message. Notifications return None (no reply)."""
    method = message.get("method")
    msg_id = message.get("id", _MISSING)
    if not isinstance(method, str):
        if msg_id is _MISSING:
            return None
        return _error(msg_id, -32600, "invalid request")

    if msg_id is _MISSING:
        return None

    params = message.get("params")
    if params is None:
        params = {}
    if not isinstance(params, dict):
        return _error(msg_id, -32602, "params must be an object")

    if method == "initialize":
        return _result(msg_id, {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
        })
    if method == "ping":
        return _result(msg_id, {})
    if method == "tools/list":
        return _result(msg_id, {"tools": TOOLS})
    if method == "tools/call":
        return _call_tool(msg_id, params, root, max_description)
    return _error(msg_id, -32601, f"method not found: {method}")


_MISSING = object()


def _call_tool(
    msg_id: Any,
    params: dict[str, Any],
    root: Path,
    max_description: int,
) -> dict[str, Any]:
    name = params.get("name")
    arguments = params.get("arguments") or {}
    if not isinstance(name, str):
        return _error(msg_id, -32602, "tools/call requires name")
    if not isinstance(arguments, dict):
        return _error(msg_id, -32602, "arguments must be an object")
    try:
        if name == "kb_catalog":
            text = _catalog_text(root, max_description)
            return _tool_result(msg_id, text)
        if name == "kb_get":
            article_id = arguments.get("id")
            if not isinstance(article_id, str) or not article_id.strip():
                return _tool_result(msg_id, "error: kb_get requires string argument id", is_error=True)
            text, is_error = _get_text(root, article_id.strip())
            return _tool_result(msg_id, text, is_error=is_error)
        return _tool_result(msg_id, f'error: unknown tool "{name}"', is_error=True)
    except Exception as exc:  # noqa: BLE001 — surface to the MCP client
        return _tool_result(msg_id, f"error: {exc}", is_error=True)


def _catalog_text(root: Path, max_description: int) -> str:
    articles = kb.load_knowledge_articles(root)
    entries = kb.catalog_entries_from_articles(articles, max_description)
    return kb.render_catalog_message(entries)


def _get_text(root: Path, article_id: str) -> tuple[str, bool]:
    if not kb.is_knowledge_id(article_id):
        return f'error: invalid knowledge id "{article_id}"', True
    article = kb.load_knowledge_article(root, article_id)
    if article is None:
        return f'error: knowledge article "{article_id}" is unknown or no longer available', True
    return kb.render_knowledge_content(article), False


def _result(msg_id: Any, result: Any) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": msg_id, "result": result}


def _error(msg_id: Any, code: int, message: str) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": msg_id, "error": {"code": code, "message": message}}


def _tool_result(msg_id: Any, text: str, is_error: bool = False) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "content": [{"type": "text", "text": text}],
    }
    if is_error:
        payload["isError"] = True
    return _result(msg_id, payload)


def run_stdio(root: Path, max_description: int = kb.DEFAULT_CATALOG_DESCRIPTION_MAX_LENGTH) -> int:
    """Read NDJSON JSON-RPC from stdin, write replies to stdout."""
    stdin = sys.stdin
    stdout = sys.stdout
    while True:
        line = stdin.readline()
        if line == "":
            return 0
        stripped = line.strip()
        if stripped == "":
            continue
        try:
            message = json.loads(stripped)
        except json.JSONDecodeError:
            reply = _error(None, -32700, "parse error")
            stdout.write(json.dumps(reply, ensure_ascii=False) + "\n")
            stdout.flush()
            continue
        if not isinstance(message, dict):
            reply = _error(None, -32600, "invalid request")
            stdout.write(json.dumps(reply, ensure_ascii=False) + "\n")
            stdout.flush()
            continue
        reply = handle_rpc(message, root, max_description)
        if reply is None:
            continue
        stdout.write(json.dumps(reply, ensure_ascii=False) + "\n")
        stdout.flush()
