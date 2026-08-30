"""Streaming parsers for the local AI session data sources."""

from __future__ import annotations

import json
import re
import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator
from urllib.parse import quote, unquote


TOOLS = ("opencode", "grok", "codex", "kimi-code", "claude")
NOISE_PREFIXES = (
    "<supermemory",
    "[SUPERMEMORY",
    "<system-reminder>",
    "<user_info>",
)
UUID_RE = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
)
WORKSPACE_PATH_RE = re.compile(r"(?:Workspace Path|工作区路径):\s*(.+)")


@dataclass(frozen=True)
class MessageRecord:
    """One normalized message emitted by a source parser."""

    tool: str
    session_id: str
    cwd: str
    title: str
    timestamp: Any
    role: str
    text: str
    source_path: str


@dataclass(frozen=True)
class SourceSpec:
    """A source file and optional metadata files used to parse it."""

    tool: str
    path: Path
    auxiliary_paths: tuple[Path, ...] = ()


def extract_text(value: Any) -> str:
    """Extract textual content from strings and common message block lists."""

    if isinstance(value, str):
        return value
    if isinstance(value, list):
        parts: list[str] = []
        for item in value:
            if isinstance(item, str):
                parts.append(item)
            elif isinstance(item, dict):
                text = item.get("text")
                if isinstance(text, str):
                    parts.append(text)
                elif "content" in item:
                    nested = extract_text(item.get("content"))
                    if nested:
                        parts.append(nested)
        return "\n".join(part for part in parts if part)
    if isinstance(value, dict):
        text = value.get("text")
        if isinstance(text, str):
            return text
        content = value.get("content")
        if content is not None:
            return extract_text(content)
    return ""


def is_noise(text: str) -> bool:
    """Return whether text is a known injected/system noise record."""

    return text.lstrip().startswith(NOISE_PREFIXES)


def normalize_role(role: Any, text: str) -> str:
    """Normalize source roles and classify injected records as system."""

    if is_noise(text):
        return "system"
    normalized = str(role or "").lower()
    if normalized in {"user", "assistant"}:
        return normalized
    return "system"


def title_from_text(text: str, limit: int = 120) -> str:
    """Create a compact fallback title from a user message."""

    first_line = text.strip().splitlines()[0] if text.strip() else ""
    first_line = re.sub(r"\s+", " ", first_line).strip()
    return first_line[:limit]


def iter_source_specs() -> Iterator[SourceSpec]:
    """Yield existing source files in deterministic tool order."""

    home = Path.home()

    opencode_db = home / ".local" / "share" / "opencode" / "opencode.db"
    if opencode_db.is_file():
        yield SourceSpec("opencode", opencode_db)

    grok_root = home / ".grok" / "sessions"
    if grok_root.is_dir():
        for chat_path in sorted(grok_root.rglob("chat_history.jsonl")):
            if chat_path.is_file():
                summary_path = chat_path.parent / "summary.json"
                auxiliary = (summary_path,) if summary_path.is_file() else ()
                yield SourceSpec("grok", chat_path, auxiliary)

    codex_root = home / ".codex" / "sessions"
    if codex_root.is_dir():
        for rollout_path in sorted(codex_root.rglob("*.jsonl")):
            if rollout_path.is_file():
                yield SourceSpec("codex", rollout_path)

    kimi_root = home / ".kimi-code" / "sessions"
    if kimi_root.is_dir():
        for wire_path in sorted(kimi_root.rglob("wire.jsonl")):
            if not wire_path.is_file():
                continue
            if wire_path.parent.parent.name != "agents":
                continue
            if not wire_path.parent.parent.parent.name.startswith("session_"):
                continue
            yield SourceSpec("kimi-code", wire_path)

    claude_root = home / ".claude" / "projects"
    if claude_root.is_dir():
        for transcript_path in sorted(claude_root.glob("*/*.jsonl")):
            if transcript_path.is_file():
                yield SourceSpec("claude", transcript_path)


def iter_records(spec: SourceSpec) -> Iterator[MessageRecord]:
    """Dispatch a source specification to its streaming parser."""

    if spec.tool == "opencode":
        yield from iter_opencode_records(spec.path)
    elif spec.tool == "grok":
        summary_path = spec.auxiliary_paths[0] if spec.auxiliary_paths else None
        yield from iter_grok_records(spec.path, summary_path)
    elif spec.tool == "codex":
        yield from iter_codex_records(spec.path)
    elif spec.tool == "kimi-code":
        yield from iter_kimi_records(spec.path)
    elif spec.tool == "claude":
        yield from iter_claude_records(spec.path)


def _record(
    tool: str,
    session_id: Any,
    cwd: Any,
    title: Any,
    timestamp: Any,
    role: Any,
    text: str,
    source_path: Path,
) -> MessageRecord | None:
    """Build a normalized record, dropping records without usable text."""

    text = text if isinstance(text, str) else str(text or "")
    if not text:
        return None
    session = str(session_id or "").strip()
    if not session:
        return None
    return MessageRecord(
        tool=tool,
        session_id=session,
        cwd=str(cwd or ""),
        title=str(title or ""),
        timestamp=timestamp,
        role=normalize_role(role, text),
        text=text,
        source_path=str(source_path),
    )


def iter_opencode_records(db_path: Path, session_id: str | None = None) -> Iterator[MessageRecord]:
    """Stream text parts from opencode using a read-only SQLite URI.

    When session_id is given, only that session's messages are streamed —
    this powers session-level incremental indexing.
    """

    uri = "file:" + quote(str(db_path), safe="/") + "?mode=ro"
    connection = sqlite3.connect(uri, uri=True)
    try:
        session_filter = "AND s.id = ?" if session_id else ""
        params = (session_id,) if session_id else ()
        query = f"""
            SELECT
                s.id AS session_id,
                s.directory AS cwd,
                s.title AS title,
                m.id AS message_id,
                m.time_created AS message_created,
                json_extract(m.data, '$.role') AS role,
                p.time_created AS part_created,
                json_extract(p.data, '$.text') AS text
            FROM session AS s
            JOIN message AS m ON m.session_id = s.id
            JOIN part AS p ON p.message_id = m.id
            WHERE json_extract(p.data, '$.text') IS NOT NULL
            {session_filter}
            ORDER BY s.id, m.time_created, m.id, p.time_created, p.id
        """
        current_key: tuple[str, str] | None = None
        current: dict[str, Any] | None = None
        text_parts: list[str] = []

        def flush() -> MessageRecord | None:
            if current is None:
                return None
            return _record(
                "opencode",
                current["session_id"],
                current["cwd"],
                current["title"],
                current["message_created"] or current["part_created"],
                current["role"],
                "\n".join(text_parts),
                db_path,
            )

        for row in connection.execute(query, params):
            key = (str(row[0]), str(row[3]))
            if current_key is not None and key != current_key:
                record = flush()
                if record is not None:
                    yield record
                text_parts = []
            if key != current_key:
                current = {
                    "session_id": row[0],
                    "cwd": row[1],
                    "title": row[2],
                    "message_created": row[4],
                    "role": row[5],
                    "part_created": row[6],
                }
                current_key = key
            text = row[7]
            if text is not None:
                text_parts.append(str(text))

        record = flush()
        if record is not None:
            yield record
    finally:
        connection.close()


def _load_summary(summary_path: Path | None) -> dict[str, Any]:
    """Load an optional Grok summary without affecting message streaming."""

    if summary_path is None:
        return {}
    try:
        with summary_path.open("r", encoding="utf-8", errors="replace") as handle:
            value = json.load(handle)
    except (OSError, json.JSONDecodeError):
        return {}
    return value if isinstance(value, dict) else {}


def iter_grok_records(
    chat_path: Path, summary_path: Path | None = None
) -> Iterator[MessageRecord]:
    """Stream user and assistant records from a Grok chat history."""

    summary = _load_summary(summary_path)
    info = summary.get("info") if isinstance(summary.get("info"), dict) else {}
    encoded_cwd = chat_path.parent.parent.name
    cwd = info.get("cwd") or summary.get("cwd") or unquote(encoded_cwd)
    title = summary.get("generated_title") or info.get("generated_title") or ""
    timestamp = summary.get("created_at") or info.get("created_at")
    session_id = chat_path.parent.name

    try:
        handle = chat_path.open("r", encoding="utf-8", errors="replace")
    except OSError:
        return
    with handle:
        for line in handle:
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(value, dict) or value.get("type") not in {
                "user",
                "assistant",
            }:
                continue
            text = extract_text(value.get("content"))
            record = _record(
                "grok",
                session_id,
                cwd,
                title,
                timestamp,
                value.get("type"),
                text,
                chat_path,
            )
            if record is not None:
                yield record


def _filename_session_id(path: Path) -> str:
    """Extract a UUID from a rollout filename when metadata is absent."""

    match = UUID_RE.search(path.name)
    return match.group(0) if match else path.stem


def iter_codex_records(rollout_path: Path) -> Iterator[MessageRecord]:
    """Stream message payloads from a Codex rollout JSONL file."""

    session_id = _filename_session_id(rollout_path)
    cwd = ""
    session_timestamp: Any = None
    title = ""
    try:
        handle = rollout_path.open("r", encoding="utf-8", errors="replace")
    except OSError:
        return
    with handle:
        for line in handle:
            # Cheap pre-filter: most lines are tool calls we never use.
            if '"message"' not in line and "session_meta" not in line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(value, dict):
                continue
            payload = value.get("payload")
            if not isinstance(payload, dict):
                continue
            line_type = value.get("type")
            if line_type == "session_meta":
                session_id = str(payload.get("id") or session_id)
                cwd = str(payload.get("cwd") or cwd)
                session_timestamp = payload.get("timestamp") or value.get("timestamp")
                continue
            if payload.get("type") != "message":
                continue
            text = extract_text(payload.get("content"))
            record = _record(
                "codex",
                session_id,
                cwd,
                title,
                value.get("timestamp") or payload.get("timestamp") or session_timestamp,
                payload.get("role"),
                text,
                rollout_path,
            )
            if record is not None:
                yield record


def _kimi_workspace_path(value: Any, text: str) -> str:
    """Find a Kimi workspace path from event metadata or injected user info."""

    if isinstance(value, dict):
        for key in ("cwd", "directory", "workspace_path"):
            candidate = value.get(key)
            if isinstance(candidate, str) and candidate:
                return candidate
    match = WORKSPACE_PATH_RE.search(text)
    return match.group(1).strip() if match else ""


def iter_kimi_records(wire_path: Path) -> Iterator[MessageRecord]:
    """Stream context.append_message records from a Kimi wire file."""

    session_dir = wire_path.parent.parent.parent
    session_id = session_dir.name
    cwd = ""
    title = ""
    try:
        handle = wire_path.open("r", encoding="utf-8", errors="replace")
    except OSError:
        return
    with handle:
        for line in handle:
            if "context.append_message" not in line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(value, dict) or value.get("type") != "context.append_message":
                continue
            message = value.get("message")
            if not isinstance(message, dict):
                continue
            text = extract_text(message.get("content"))
            if not text:
                continue
            cwd = _kimi_workspace_path(message, text) or cwd
            origin = message.get("origin")
            if not isinstance(origin, dict):
                origin = value.get("origin") if isinstance(value.get("origin"), dict) else {}
            role = message.get("role")
            if origin.get("kind") == "user":
                role = "user"
            record = _record(
                "kimi-code",
                session_id,
                cwd,
                title,
                value.get("time") or message.get("time"),
                role,
                text,
                wire_path,
            )
            if record is not None:
                yield record


def iter_claude_records(transcript_path: Path) -> Iterator[MessageRecord]:
    """Stream user and assistant messages from a Claude transcript."""

    fallback_session_id = transcript_path.stem
    try:
        handle = transcript_path.open("r", encoding="utf-8", errors="replace")
    except OSError:
        return
    with handle:
        for line in handle:
            if '"message"' not in line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(value, dict):
                continue
            message = value.get("message")
            if not isinstance(message, dict):
                continue
            outer_type = value.get("type")
            if outer_type not in {"user", "assistant"}:
                continue
            text = extract_text(message.get("content"))
            session_id = value.get("sessionId") or value.get("session_id") or fallback_session_id
            cwd = value.get("cwd") or value.get("directory") or ""
            timestamp = value.get("timestamp") or message.get("timestamp")
            record = _record(
                "claude",
                session_id,
                cwd,
                value.get("title") or "",
                timestamp,
                message.get("role") or outer_type,
                text,
                transcript_path,
            )
            if record is not None:
                yield record
