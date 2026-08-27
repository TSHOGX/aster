"""Plain-text Markdown knowledge base: catalog + on-demand loader.

Mirrors the DeepSeek Harness example plugin (`@deepseek-ai/dsh-knowledge`):
articles live as unencrypted Markdown under a `knowledge/` tree, the model sees
only an id/description/tags catalog, and `kb get <id>` loads one full body.

Malformed files and a missing root yield an empty catalog rather than a crash.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Optional, Sequence, Tuple

KNOWLEDGE_ID_PATTERN = re.compile(r"^[a-z0-9]+(?:[/-][a-z0-9]+)*$")
DEFAULT_ROOT_NAME = "knowledge"
DEFAULT_CATALOG_DESCRIPTION_MAX_LENGTH = 500
SANDBOX_KNOWLEDGE_ROOT = Path("/agents/knowledge")


@dataclass(frozen=True)
class KnowledgeArticle:
    """One discoverable Markdown article after frontmatter parse."""

    id: str
    description: str
    path: str
    body: str
    title: Optional[str] = None
    tags: Tuple[str, ...] = ()


@dataclass(frozen=True)
class KnowledgeCatalogEntry:
    """Catalog row published to the model (no body)."""

    id: str
    description: str
    tags: Tuple[str, ...] = ()


def is_knowledge_id(value: str) -> bool:
    return bool(value) and KNOWLEDGE_ID_PATTERN.fullmatch(value) is not None


def escape_text(value: str) -> str:
    return value.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def resolve_knowledge_root(root: Optional[str] = None, cwd: Optional[str] = None) -> Path:
    """Resolve the knowledge root.

    Order: explicit `root` (absolute or cwd-relative), else `KNOWLEDGE_ROOT`,
    else `/agents/knowledge` when that directory exists, else `<cwd>/knowledge`.
    """
    base = Path(cwd) if cwd else Path.cwd()
    if root is not None and root.strip():
        candidate = Path(root.strip())
        return candidate if candidate.is_absolute() else (base / candidate).resolve()
    env = os.environ.get("KNOWLEDGE_ROOT", "").strip()
    if env:
        candidate = Path(env)
        return candidate if candidate.is_absolute() else (base / candidate).resolve()
    if SANDBOX_KNOWLEDGE_ROOT.is_dir():
        return SANDBOX_KNOWLEDGE_ROOT
    return (base / DEFAULT_ROOT_NAME).resolve()


def article_path_for_id(root: Path, article_id: str) -> Optional[Path]:
    if not is_knowledge_id(article_id):
        return None
    candidate = (root / Path(*article_id.split("/"))).with_suffix(".md").resolve()
    if not _is_path_inside_root(root, candidate):
        return None
    return candidate


def load_knowledge_articles(root: Path) -> list[KnowledgeArticle]:
    if not root.is_dir():
        return []
    articles: list[KnowledgeArticle] = []
    for path in _list_markdown_files(root):
        try:
            raw = path.read_text(encoding="utf-8")
        except OSError:
            continue
        parsed = parse_knowledge_document(raw, path, root)
        if parsed is not None:
            articles.append(parsed)
    articles.sort(key=lambda article: article.id)
    return articles


def load_knowledge_article(root: Path, article_id: str) -> Optional[KnowledgeArticle]:
    path = article_path_for_id(root, article_id)
    if path is None:
        return None
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError:
        return None
    return parse_knowledge_document(raw, path, root)


def parse_knowledge_document(raw: str, path: Path, root: Path) -> Optional[KnowledgeArticle]:
    frontmatter = parse_frontmatter(raw)
    if frontmatter is None:
        return None
    data, body = frontmatter
    description = _read_required_string(data, "description")
    if description is None:
        return None
    title = _read_optional_string(data, "title")
    tags = _read_string_array(data, "tags")
    if tags is None:
        tags = []
    explicit_id = _read_optional_string(data, "id")
    default_id = _default_id_from_path(path, root)
    article_id = explicit_id or default_id
    if article_id is None or not is_knowledge_id(article_id):
        return None
    cleaned = body.lstrip("\ufeff").lstrip("\n").rstrip()
    return KnowledgeArticle(
        id=article_id,
        description=description,
        path=str(path),
        body=cleaned,
        title=title,
        tags=tuple(tags),
    )


def catalog_entries_from_articles(
    articles: Sequence[KnowledgeArticle],
    description_max_length: int = DEFAULT_CATALOG_DESCRIPTION_MAX_LENGTH,
) -> list[KnowledgeCatalogEntry]:
    return [
        KnowledgeCatalogEntry(
            id=article.id,
            description=catalog_description(article.description, description_max_length),
            tags=article.tags,
        )
        for article in articles
    ]


def catalog_description(value: str, max_length: int) -> str:
    normalized = re.sub(r"\s+", " ", value).strip()
    if len(normalized) <= max_length:
        return normalized
    return f"{normalized[: max_length - 3]}..."


def render_catalog_entry_lines(entries: Sequence[KnowledgeCatalogEntry]) -> list[str]:
    lines: list[str] = []
    for entry in entries:
        tag_suffix = f" [{', '.join(entry.tags)}]" if entry.tags else ""
        lines.append(f"- `{entry.id}`: {escape_text(entry.description)}{tag_suffix}")
    return lines


def render_catalog_message(entries: Sequence[KnowledgeCatalogEntry], *, update: bool = False) -> str:
    header = (
        "The available knowledge catalog changed. This complete catalog replaces "
        "every earlier available-knowledge list in this session:"
        if update
        else "A knowledge base of plain-text Markdown articles is available in this session:"
    )
    if update:
        availability = (
            "No knowledge articles are currently available through `kb get`. "
            "Do not use ids from earlier knowledge catalogs."
            if len(entries) == 0
            else "Use only ids in this replacement catalog. If the user question "
            "matches a listed description, call `kb get` with that exact id before answering."
        )
    else:
        availability = (
            "If the user question matches an article description, run `kb get <id>` "
            "with that exact id before answering. This catalog contains summaries "
            "only; do not invent article bodies."
        )
    return "\n".join(
        [
            "<system-reminder>",
            header,
            "",
            "<available_knowledge>",
            *render_catalog_entry_lines(entries),
            "</available_knowledge>",
            "",
            availability,
            "</system-reminder>",
        ]
    )


def digest_catalog_entries(entries: Sequence[KnowledgeCatalogEntry]) -> str:
    canonical = "\n".join(
        json.dumps([entry.id, entry.description, list(entry.tags)], ensure_ascii=False)
        for entry in entries
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def render_knowledge_content(article: KnowledgeArticle) -> str:
    lines = [f'<knowledge_content id="{escape_text(article.id)}">']
    if article.title is not None:
        lines.append(f"<title>{escape_text(article.title)}</title>")
    if article.tags:
        lines.append(f"<tags>{escape_text(', '.join(article.tags))}</tags>")
    lines.extend(["<knowledge_body>", article.body, "</knowledge_body>", "</knowledge_content>"])
    return "\n".join(lines)


def article_to_json(article: KnowledgeArticle) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "id": article.id,
        "description": article.description,
        "tags": list(article.tags),
        "path": article.path,
        "body": article.body,
    }
    if article.title is not None:
        payload["title"] = article.title
    return payload


def catalog_to_json(root: Path, entries: Sequence[KnowledgeCatalogEntry]) -> dict[str, Any]:
    return {
        "root": str(root),
        "entries": [
            {"id": entry.id, "description": entry.description, "tags": list(entry.tags)}
            for entry in entries
        ],
    }


def parse_frontmatter(raw: str) -> Optional[Tuple[dict[str, Any], str]]:
    first_line_end = raw.find("\n")
    if first_line_end < 0:
        return None
    first_line = raw[:first_line_end].replace("\r", "")
    if first_line != "---":
        return None
    start = first_line_end + 1
    closing = _find_closing_frontmatter(raw, start)
    if closing is None:
        return None
    yaml_text = raw[start : closing[0]]
    data = _parse_closed_frontmatter_yaml(yaml_text)
    if data is None:
        return None
    return data, raw[closing[1] :]


def _find_closing_frontmatter(raw: str, start: int) -> Optional[Tuple[int, int]]:
    line_start = start
    while line_start <= len(raw):
        next_newline = raw.find("\n", line_start)
        line_end = len(raw) if next_newline < 0 else next_newline
        line = raw[line_start:line_end].replace("\r", "")
        if line == "---":
            body_start = len(raw) if next_newline < 0 else next_newline + 1
            return line_start, body_start
        if next_newline < 0:
            return None
        line_start = next_newline + 1
    return None


def _parse_closed_frontmatter_yaml(yaml_text: str) -> Optional[dict[str, Any]]:
    data: dict[str, Any] = {}
    lines = yaml_text.splitlines()
    index = 0
    key_re = re.compile(r"^([A-Za-z][A-Za-z0-9_-]*):\s*(.*)$")
    while index < len(lines):
        line = lines[index]
        if line.strip() == "" or line.lstrip().startswith("#"):
            index += 1
            continue
        match = key_re.match(line)
        if match is None:
            return None
        key, rest = match.group(1), match.group(2)
        if rest in ("", "|", ">"):
            if key != "tags":
                return None
            tags: list[str] = []
            index += 1
            while index < len(lines):
                item = lines[index]
                if not re.match(r"^\s+-\s+", item):
                    break
                value = _parse_scalar(re.sub(r"^\s+-\s+", "", item).strip())
                if value is None:
                    return None
                tags.append(value)
                index += 1
            data[key] = tags
            continue
        if rest.startswith("[") and rest.endswith("]"):
            inner = rest[1:-1].strip()
            if inner == "":
                data[key] = []
            else:
                values: list[str] = []
                for part in _split_comma_list(inner):
                    value = _parse_scalar(part.strip())
                    if value is None:
                        return None
                    values.append(value)
                data[key] = values
            index += 1
            continue
        value = _parse_scalar(rest.strip())
        if value is None:
            return None
        data[key] = value
        index += 1
    return data


def _split_comma_list(inner: str) -> list[str]:
    parts: list[str] = []
    current = ""
    in_quote: str | None = None
    for ch in inner:
        if in_quote is not None:
            current += ch
            if ch == in_quote:
                in_quote = None
            continue
        if ch in ('"', "'"):
            in_quote = ch
            current += ch
            continue
        if ch == ",":
            parts.append(current)
            current = ""
            continue
        current += ch
    parts.append(current)
    return parts


def _parse_scalar(raw: str) -> Optional[str]:
    if raw == "":
        return ""
    if (raw.startswith('"') and raw.endswith('"')) or (raw.startswith("'") and raw.endswith("'")):
        return raw[1:-1]
    if raw in ("|", ">") or raw.startswith("{") or raw.startswith("["):
        return None
    return raw


def _read_required_string(data: Mapping[str, Any], key: str) -> Optional[str]:
    value = data.get(key)
    if not isinstance(value, str):
        return None
    trimmed = value.strip()
    return trimmed if trimmed else None


def _read_optional_string(data: Mapping[str, Any], key: str) -> Optional[str]:
    if key not in data:
        return None
    return _read_required_string(data, key)


def _read_string_array(data: Mapping[str, Any], key: str) -> Optional[list[str]]:
    if key not in data:
        return None
    value = data[key]
    if not isinstance(value, list):
        return None
    out: list[str] = []
    for item in value:
        if not isinstance(item, str):
            return None
        trimmed = item.strip()
        if not trimmed:
            return None
        out.append(trimmed)
    return out


def _list_markdown_files(root: Path) -> list[Path]:
    out: list[Path] = []
    stack = [root]
    while stack:
        directory = stack.pop()
        try:
            entries = list(directory.iterdir())
        except OSError:
            continue
        for entry in entries:
            if entry.name.startswith("."):
                continue
            try:
                if entry.is_dir():
                    stack.append(entry)
                elif entry.is_file() and entry.suffix == ".md":
                    out.append(entry)
            except OSError:
                continue
    out.sort()
    return out


def _default_id_from_path(path: Path, root: Path) -> Optional[str]:
    try:
        rel = path.resolve().relative_to(root.resolve())
    except ValueError:
        return None
    if rel.suffix != ".md":
        return None
    article_id = rel.with_suffix("").as_posix()
    return article_id if is_knowledge_id(article_id) else None


def _is_path_inside_root(root: Path, candidate: Path) -> bool:
    try:
        candidate.resolve().relative_to(root.resolve())
        return True
    except ValueError:
        return False
