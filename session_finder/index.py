"""SQLite index construction and search helpers for session-finder."""

from __future__ import annotations

import re
import sqlite3
from collections import OrderedDict
from datetime import date, datetime, timezone
from pathlib import Path
from typing import Any, Iterable, Iterator
from urllib.parse import quote

from . import parsers
from .parsers import MessageRecord, SourceSpec


DEFAULT_INDEX_PATH = Path.home() / ".cache" / "session-finder" / "index.db"
CJK_RE = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff]")
QUERY_TERM_RE = re.compile(r"[A-Za-z0-9_]+|[\u3400-\u4dbf\u4e00-\u9fff]+")

FTS_TRIGGERS_SQL = """
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text)
        VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text)
        VALUES ('delete', old.id, old.text);
    INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_tri(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_tri(messages_tri, rowid, text)
        VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_au AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_tri(messages_tri, rowid, text)
        VALUES ('delete', old.id, old.text);
    INSERT INTO messages_tri(rowid, text) VALUES (new.id, new.text);
END;
"""

_FTS_TRIGGER_NAMES = (
    "messages_ai", "messages_ad", "messages_au",
    "messages_tri_ai", "messages_tri_ad", "messages_tri_au",
)


def _drop_fts_triggers(connection: sqlite3.Connection) -> None:
    """Drop per-row FTS triggers so bulk inserts skip per-row index updates."""

    for name in _FTS_TRIGGER_NAMES:
        connection.execute(f"DROP TRIGGER IF EXISTS {name}")



def open_index(db_path: Path = DEFAULT_INDEX_PATH) -> sqlite3.Connection:
    """Open the writable local search index and configure foreign keys."""

    db_path.parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(str(db_path))
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA foreign_keys = ON")
    connection.execute("PRAGMA busy_timeout = 5000")
    # The index is a rebuildable cache: favor speed over durability.
    connection.execute("PRAGMA journal_mode = WAL")
    connection.execute("PRAGMA synchronous = OFF")
    connection.execute("PRAGMA temp_store = MEMORY")
    connection.execute("PRAGMA mmap_size = 268435456")  # 256 MB
    connection.execute("PRAGMA cache_size = -65536")    # 64 MB page cache
    return connection


def initialize_schema(connection: sqlite3.Connection) -> None:
    """Create the session, message, source, and FTS5 tables."""

    connection.executescript(
        """
        CREATE TABLE IF NOT EXISTS sessions (
            id INTEGER PRIMARY KEY,
            tool TEXT NOT NULL,
            session_id TEXT NOT NULL,
            cwd TEXT NOT NULL DEFAULT '',
            title TEXT NOT NULL DEFAULT '',
            created REAL,
            updated REAL,
            source_path TEXT NOT NULL,
            mtime REAL NOT NULL DEFAULT 0,
            size INTEGER NOT NULL DEFAULT 0
        );
        CREATE INDEX IF NOT EXISTS sessions_tool_id
            ON sessions(tool, session_id);
        CREATE INDEX IF NOT EXISTS sessions_source_path
            ON sessions(source_path);

        -- Keep older local indexes usable when the schema gains source metadata.
        """
    )
    columns = {
        str(row[1]) for row in connection.execute("PRAGMA table_info(sessions)")
    }
    if "mtime" not in columns:
        connection.execute("ALTER TABLE sessions ADD COLUMN mtime REAL NOT NULL DEFAULT 0")
    if "size" not in columns:
        connection.execute("ALTER TABLE sessions ADD COLUMN size INTEGER NOT NULL DEFAULT 0")
    connection.executescript(
        """
        CREATE TABLE IF NOT EXISTS messages (
            id INTEGER PRIMARY KEY,
            session_pk INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
            role TEXT NOT NULL,
            ts REAL,
            text TEXT NOT NULL
        );
        CREATE INDEX IF NOT EXISTS messages_session
            ON messages(session_pk, ts, id);
        CREATE INDEX IF NOT EXISTS messages_role
            ON messages(role);

        CREATE TABLE IF NOT EXISTS sources (
            source_path TEXT PRIMARY KEY,
            tool TEXT NOT NULL,
            mtime REAL NOT NULL,
            size INTEGER NOT NULL,
            skipped INTEGER NOT NULL DEFAULT 0
        );

        CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
            text,
            content='messages',
            content_rowid='id',
            tokenize='unicode61'
        );

        -- Trigram FTS replaces slow LIKE '%term%' scans for substring/CJK search.
        CREATE VIRTUAL TABLE IF NOT EXISTS messages_tri USING fts5(
            text,
            content='messages',
            content_rowid='id',
            tokenize='trigram'
        );

        -- Session-level change tracking for opencode's shared SQLite store.
        CREATE TABLE IF NOT EXISTS opencode_sessions (
            session_id TEXT PRIMARY KEY,
            time_updated INTEGER NOT NULL
        );
        """
        + FTS_TRIGGERS_SQL
    )
    # Backfill the trigram index when it exists but lags behind messages
    # (e.g. it was added to an older index file).
    messages_count = connection.execute("SELECT count(*) FROM messages").fetchone()[0]
    tri_count = connection.execute("SELECT count(*) FROM messages_tri").fetchone()[0]
    if tri_count != messages_count:
        connection.execute("INSERT INTO messages_tri(messages_tri) VALUES ('rebuild')")
    connection.commit()


def clear_index(connection: sqlite3.Connection) -> None:
    """Remove all indexed data while preserving the schema."""

    connection.execute("DELETE FROM sources")
    connection.execute("DELETE FROM sessions")
    connection.execute("DELETE FROM opencode_sessions")
    connection.commit()


def source_signature(spec: SourceSpec) -> tuple[float, int]:
    """Return an mtime/size signature for a source and its metadata files."""

    paths = [spec.path, *spec.auxiliary_paths]
    if spec.tool == "opencode":
        paths.extend(
            [
                spec.path.with_name(spec.path.name + "-wal"),
                spec.path.with_name(spec.path.name + "-shm"),
            ]
        )
    mtimes: list[float] = []
    total_size = 0
    for path in paths:
        try:
            stat = path.stat()
        except OSError:
            continue
        mtimes.append(stat.st_mtime)
        total_size += stat.st_size
    return (max(mtimes, default=0.0), total_size)


def _timestamp_to_epoch(value: Any) -> float | None:
    """Convert epoch seconds/milliseconds or ISO timestamps to UTC seconds."""

    if value is None:
        return None
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        number = float(value)
        if number > 10_000_000_000:
            number /= 1000.0
        return number
    text = str(value).strip()
    if not text:
        return None
    try:
        number = float(text)
    except ValueError:
        number = None
    if number is not None:
        if number > 10_000_000_000:
            number /= 1000.0
        return number
    normalized = text[:-1] + "+00:00" if text.endswith("Z") else text
    try:
        parsed = datetime.fromisoformat(normalized)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.timestamp()


def format_timestamp(value: Any) -> str:
    """Format a stored epoch timestamp as a compact UTC ISO string."""

    epoch = _timestamp_to_epoch(value)
    if epoch is None:
        return "-"
    return datetime.fromtimestamp(epoch, timezone.utc).isoformat(timespec="seconds").replace(
        "+00:00", "Z"
    )


def _merge_min(current: float | None, candidate: float | None) -> float | None:
    if current is None:
        return candidate
    if candidate is None:
        return current
    return min(current, candidate)


def _merge_max(current: float | None, candidate: float | None) -> float | None:
    if current is None:
        return candidate
    if candidate is None:
        return current
    return max(current, candidate)


def _get_or_create_session(
    connection: sqlite3.Connection,
    cache: dict[tuple[str, str, str], int],
    record: MessageRecord,
) -> int:
    """Get or create a source-specific session row and merge metadata."""

    key = (record.tool, record.session_id, record.source_path)
    session_pk = cache.get(key)
    timestamp = _timestamp_to_epoch(record.timestamp)
    if session_pk is None:
        row = connection.execute(
            """
            SELECT id, cwd, title, created, updated
            FROM sessions
            WHERE tool = ? AND session_id = ? AND source_path = ?
            """,
            key,
        ).fetchone()
        if row is None:
            title = record.title or (
                parsers.title_from_text(record.text) if record.role == "user" else ""
            )
            cursor = connection.execute(
                """
                INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
                VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    record.tool,
                    record.session_id,
                    record.cwd,
                    title,
                    timestamp,
                    timestamp,
                    record.source_path,
                ),
            )
            session_pk = int(cursor.lastrowid)
            cache[key] = session_pk
            return session_pk
        session_pk = int(row[0])
        cache[key] = session_pk

    row = connection.execute(
        "SELECT cwd, title, created, updated FROM sessions WHERE id = ?",
        (session_pk,),
    ).fetchone()
    if row is None:
        cache.pop(key, None)
        return _get_or_create_session(connection, cache, record)
    cwd = row[0] or record.cwd
    title = row[1] or record.title
    if not title and record.role == "user":
        title = parsers.title_from_text(record.text)
    created = _merge_min(row[2], timestamp)
    updated = _merge_max(row[3], timestamp)
    if cwd != row[0] or title != row[1] or created != row[2] or updated != row[3]:
        connection.execute(
            """
            UPDATE sessions
            SET cwd = ?, title = ?, created = ?, updated = ?
            WHERE id = ?
            """,
            (cwd, title, created, updated, session_pk),
        )
    return session_pk


def _upsert_source(
    connection: sqlite3.Connection,
    spec: SourceSpec,
    signature: tuple[float, int],
    skipped: bool,
) -> None:
    connection.execute(
        """
        INSERT INTO sources(source_path, tool, mtime, size, skipped)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(source_path) DO UPDATE SET
            tool = excluded.tool,
            mtime = excluded.mtime,
            size = excluded.size,
            skipped = excluded.skipped
        """,
        (str(spec.path), spec.tool, signature[0], signature[1], int(skipped)),
    )


def _source_is_unchanged(
    connection: sqlite3.Connection,
    spec: SourceSpec,
    signature: tuple[float, int],
) -> bool:
    row = connection.execute(
        "SELECT mtime, size, skipped FROM sources WHERE source_path = ?",
        (str(spec.path),),
    ).fetchone()
    return bool(
        row
        and float(row[0]) == signature[0]
        and int(row[1]) == signature[1]
        and int(row[2]) == 0
    )


def _insert_records(connection: sqlite3.Connection, records: Iterable[MessageRecord]) -> tuple[int, int]:
    """Insert a record stream with one executemany batch. Returns (sessions, messages)."""

    cache: dict[tuple[str, str, str], int] = {}
    batch: list[tuple[int, str, float | None, str]] = []
    for record in records:
        session_pk = _get_or_create_session(connection, cache, record)
        batch.append(
            (session_pk, record.role, _timestamp_to_epoch(record.timestamp), record.text)
        )
    connection.executemany(
        "INSERT INTO messages(session_pk, role, ts, text) VALUES (?, ?, ?, ?)",
        batch,
    )
    return (len(cache), len(batch))


def _index_source(
    connection: sqlite3.Connection,
    spec: SourceSpec,
    signature: tuple[float, int],
) -> tuple[int, int]:
    """Replace one changed source in a single transaction."""

    connection.execute("DELETE FROM sessions WHERE source_path = ?", (str(spec.path),))
    sessions_count, message_count = _insert_records(connection, parsers.iter_records(spec))
    connection.execute(
        "UPDATE sessions SET mtime = ?, size = ? WHERE source_path = ?",
        (signature[0], signature[1], str(spec.path)),
    )
    if spec.tool == "grok" and spec.auxiliary_paths:
        summary = parsers._load_summary(spec.auxiliary_paths[0])
        info = summary.get("info") if isinstance(summary.get("info"), dict) else {}
        created = _timestamp_to_epoch(summary.get("created_at") or info.get("created_at"))
        updated = _timestamp_to_epoch(summary.get("updated_at") or info.get("updated_at"))
        cwd = summary.get("cwd") or info.get("cwd")
        title = summary.get("generated_title") or info.get("generated_title")
        connection.execute(
            """
            UPDATE sessions
            SET created = COALESCE(?, created),
                updated = COALESCE(?, updated),
                cwd = CASE WHEN ? THEN ? ELSE cwd END,
                title = CASE WHEN ? THEN ? ELSE title END
            WHERE source_path = ?
            """,
            (
                created,
                updated,
                bool(cwd),
                str(cwd or ""),
                bool(title),
                str(title or ""),
                str(spec.path),
            ),
        )
    _upsert_source(connection, spec, signature, skipped=False)
    return (sessions_count, message_count)


def _index_opencode(
    connection: sqlite3.Connection,
    spec: SourceSpec,
    signature: tuple[float, int],
) -> tuple[int, int]:
    """Session-level incremental indexing for opencode's shared SQLite store.

    Diffs the source's session table (id, time_updated) against our tracking
    table so a db change only re-parses the sessions that actually changed.
    """

    uri = "file:" + quote(str(spec.path), safe="/") + "?mode=ro"
    source = sqlite3.connect(uri, uri=True)
    try:
        current = {
            str(row[0]): int(row[1] or 0)
            for row in source.execute("SELECT id, time_updated FROM session")
        }
    finally:
        source.close()

    tracked = {
        str(row[0]): int(row[1])
        for row in connection.execute(
            "SELECT session_id, time_updated FROM opencode_sessions"
        )
    }
    removed = [sid for sid in tracked if sid not in current]
    changed = [sid for sid, updated in current.items() if tracked.get(sid) != updated]

    for sid in removed + changed:
        connection.execute(
            "DELETE FROM sessions WHERE tool = 'opencode' AND session_id = ?", (sid,)
        )
        connection.execute(
            "DELETE FROM opencode_sessions WHERE session_id = ?", (sid,)
        )

    sessions_count = 0
    message_count = 0
    for sid in changed:
        parsed_sessions, parsed_messages = _insert_records(
            connection, parsers.iter_opencode_records(spec.path, session_id=sid)
        )
        sessions_count += parsed_sessions
        message_count += parsed_messages
        connection.execute(
            "INSERT OR REPLACE INTO opencode_sessions(session_id, time_updated)"
            " VALUES (?, ?)",
            (sid, current[sid]),
        )
    _upsert_source(connection, spec, signature, skipped=False)
    return (sessions_count, message_count)


def index_all(
    full: bool = False,
    db_path: Path = DEFAULT_INDEX_PATH,
) -> dict[str, Any]:
    """Build or incrementally update the local index from all known tools."""

    connection = open_index(db_path)
    initialize_schema(connection)
    if full:
        # Per-row FTS triggers would fire for every bulk insert; drop them
        # and rebuild both FTS indexes in one pass at the end instead.
        _drop_fts_triggers(connection)
        clear_index(connection)

    processed = 0
    unchanged = 0
    errors = 0
    for spec in parsers.iter_source_specs():
        signature = source_signature(spec)
        if spec.tool == "opencode":
            # Session-level diff is cheap enough to run every time.
            processed += 1
            try:
                with connection:
                    _index_opencode(connection, spec, signature)
            except (OSError, sqlite3.Error, ValueError) as exc:
                errors += 1
                print(f"warning: failed to index {spec.path}: {exc}", flush=True)
            continue
        if not full and _source_is_unchanged(connection, spec, signature):
            unchanged += 1
            continue
        processed += 1
        try:
            with connection:
                _index_source(connection, spec, signature)
        except (OSError, sqlite3.Error, ValueError) as exc:
            errors += 1
            print(f"warning: failed to index {spec.path}: {exc}", flush=True)

    if full:
        with connection:
            connection.execute(
                "INSERT INTO messages_fts(messages_fts) VALUES('rebuild')"
            )
            connection.execute(
                "INSERT INTO messages_tri(messages_tri) VALUES('rebuild')"
            )
            connection.executescript(FTS_TRIGGERS_SQL)

    stats = get_stats(connection)
    source_stats = {
        "processed": processed,
        "unchanged": unchanged,
        "errors": errors,
    }
    connection.close()
    return {"tools": stats, "sources": source_stats, "db_path": str(db_path)}


def get_stats(connection: sqlite3.Connection) -> dict[str, dict[str, int]]:
    """Return distinct session and message counts for every supported tool."""

    result = {tool: {"sessions": 0, "messages": 0} for tool in parsers.TOOLS}
    rows = connection.execute(
        """
        SELECT s.tool, COUNT(DISTINCT s.session_id) AS sessions, COUNT(m.id) AS messages
        FROM sessions AS s
        LEFT JOIN messages AS m ON m.session_pk = s.id
        GROUP BY s.tool
        """
    )
    for row in rows:
        result[str(row[0])] = {"sessions": int(row[1]), "messages": int(row[2])}
    return result


def _escape_like(value: str) -> str:
    return value.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")


def _query_terms(query: str) -> list[str]:
    terms: list[str] = []
    for term in QUERY_TERM_RE.findall(query):
        if term not in terms:
            terms.append(term)
    return terms


def _fts_query(terms: Iterable[str]) -> str:
    return " AND ".join('"' + term.replace('"', '""') + '"' for term in terms)


def _manual_snippet(text: str, query: str, max_chars: int = 200) -> str:
    """Return a bounded snippet, preferring the first query-term occurrence."""

    compact = re.sub(r"\s+", " ", text).strip()
    if len(compact) <= max_chars:
        return compact
    terms = _query_terms(query) or [query.strip()]
    lowered = compact.casefold()
    position = -1
    for term in terms:
        found = lowered.find(term.casefold())
        if found >= 0 and (position < 0 or found < position):
            position = found
    if position < 0:
        return compact[: max_chars - 1] + "…"
    start = max(0, position - max_chars // 3)
    end = min(len(compact), start + max_chars)
    if end - start < max_chars:
        start = max(0, end - max_chars)
    snippet = compact[start:end]
    if start > 0:
        snippet = "…" + snippet[1:]
    if end < len(compact):
        snippet = snippet[:-1] + "…"
    return snippet[:max_chars]


def search_index(
    connection: sqlite3.Connection,
    query: str,
    tool: str | None = None,
    cwd: str | None = None,
    after: str | None = None,
    limit: int = 20,
    include_system: bool = False,
) -> list[dict[str, Any]]:
    """Search FTS5 plus Chinese substring fallback and aggregate by session."""

    if limit <= 0:
        raise ValueError("limit must be positive")
    query = query.strip()
    if not query:
        return []
    terms = _query_terms(query)
    cjk_query = bool(CJK_RE.search(query))
    if not terms and not cjk_query:
        return []

    if not terms:
        return []

    where: list[str] = []
    filter_params: list[Any] = []
    if not include_system:
        where.append("m.role IN ('user', 'assistant')")
    if tool:
        where.append("s.tool = ?")
        filter_params.append(tool)
    if cwd:
        where.append("s.cwd LIKE ? ESCAPE '\\'")
        filter_params.append("%" + _escape_like(cwd) + "%")
    if after:
        after_epoch = _timestamp_to_epoch(after)
        if after_epoch is None:
            try:
                after_epoch = datetime.combine(
                    date.fromisoformat(after), datetime.min.time(), timezone.utc
                ).timestamp()
            except ValueError as exc:
                raise ValueError("after must be YYYY-MM-DD") from exc
        where.append("m.ts IS NOT NULL AND m.ts >= ?")
        filter_params.append(after_epoch)
    extra_where = " AND ".join(where)
    extra_clause = f" AND {extra_where}" if extra_where else ""

    # Cap per-term matches so common words cannot blow up Python-side work.
    match_cap = 50000
    matched_messages: dict[int, dict[str, Any]] = {}

    def add_match(row: sqlite3.Row, term: str) -> None:
        """Merge one term hit into the message-level result set (no text load)."""

        message_id = int(row["message_id"])
        match = matched_messages.get(message_id)
        if match is None:
            match = {key: row[key] for key in row.keys()}
            match["matched_terms"] = set()
            matched_messages[message_id] = match
        match["matched_terms"].add(term)

    # Columns fetched per hit. Deliberately excludes m.text: full message
    # bodies (some are MBs) are loaded on demand only for final results.
    hit_columns = """
                m.id AS message_id,
                s.tool AS tool,
                s.session_id AS session_id,
                s.title AS title,
                s.cwd AS cwd,
                s.created AS session_created,
                s.updated AS session_updated,
                s.source_path AS source_path,
                m.ts AS message_ts,
                m.role AS role
    """

    for term in terms:
        # NB: FTS must run as an IN-subquery. Joining the FTS table directly
        # makes SQLite pick a disastrous plan (seconds → milliseconds).
        fts_sql = f"""
            SELECT {hit_columns}
            FROM messages AS m
            JOIN sessions AS s ON s.id = m.session_pk
            WHERE m.id IN (
                SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?
            ){extra_clause}
            LIMIT {match_cap}
        """
        fts_params = [_fts_query([term]), *filter_params]
        for row in connection.execute(fts_sql, fts_params):
            add_match(row, term)

        # Trigram FTS catches substrings that unicode61 misses (CJK runs,
        # ASCII words embedded in Chinese text). LIKE only for short terms
        # that trigram cannot match (< 3 chars).
        if len(term) >= 3:
            tri_sql = f"""
                SELECT {hit_columns}
                FROM messages AS m
                JOIN sessions AS s ON s.id = m.session_pk
                WHERE m.id IN (
                    SELECT rowid FROM messages_tri WHERE messages_tri MATCH ?
                ){extra_clause}
                LIMIT {match_cap}
            """
            tri_params = ['"' + term.replace('"', '""') + '"', *filter_params]
            for row in connection.execute(tri_sql, tri_params):
                add_match(row, term)
        else:
            like_sql = f"""
                SELECT {hit_columns}
                FROM messages AS m
                JOIN sessions AS s ON s.id = m.session_pk
                WHERE m.text LIKE ? ESCAPE '\\'{extra_clause}
                LIMIT {match_cap}
            """
            like_params = ["%" + _escape_like(term) + "%", *filter_params]
            for row in connection.execute(like_sql, like_params):
                add_match(row, term)

    groups: OrderedDict[tuple[str, str], dict[str, Any]] = OrderedDict()
    for message_id, match in matched_messages.items():
        key = (str(match["tool"]), str(match["session_id"]))
        group = groups.get(key)
        if group is None:
            group = {
                "tool": match["tool"],
                "session_id": match["session_id"],
                "title": match["title"] or "",
                "cwd": match["cwd"] or "",
                "created": match["session_created"],
                "updated": match["session_updated"],
                "message_count": 0,
                "snippets": [],
                "source_paths": [],
                "_matched_terms": set(),
                "_message_ids": [],
                "_updated_epoch": _timestamp_to_epoch(match["session_updated"]),
            }
            groups[key] = group
        group["message_count"] += 1
        group["_matched_terms"].update(match["matched_terms"])
        if len(group["_message_ids"]) < 6:
            group["_message_ids"].append(message_id)
        group["_updated_epoch"] = _merge_max(
            group["_updated_epoch"], _timestamp_to_epoch(match["session_updated"])
        )
        if not group["title"] and match["title"]:
            group["title"] = match["title"]
        if not group["cwd"] and match["cwd"]:
            group["cwd"] = match["cwd"]
        group["created"] = _merge_min(group["created"], match["session_created"])
        group["updated"] = _merge_max(group["updated"], match["session_updated"])
        source_path = str(match["source_path"])
        if source_path not in group["source_paths"]:
            group["source_paths"].append(source_path)

    required_terms = set(terms)
    results = []
    for result in groups.values():
        if not required_terms.issubset(result["_matched_terms"]):
            continue
        result["created"] = format_timestamp(result["created"])
        result["updated"] = format_timestamp(result["updated"])
        results.append(result)
    results.sort(
        key=lambda item: (
            -len(item["_matched_terms"]),
            -int(item["message_count"]),
            -float(item["_updated_epoch"] or 0),
            item["tool"],
            item["session_id"],
        )
    )
    results = results[:limit]

    # Load full text only for the few messages behind the final results and
    # build snippets from them.
    for result in results:
        ids = result.pop("_message_ids")
        result.pop("_matched_terms")
        result.pop("_updated_epoch")
        if not ids:
            continue
        placeholders = ",".join("?" for _ in ids)
        rows = connection.execute(
            f"SELECT id, text FROM messages WHERE id IN ({placeholders}) ORDER BY id",
            ids,
        )
        for row in rows:
            snippet = _manual_snippet(str(row["text"]), query)
            if snippet and snippet not in result["snippets"]:
                result["snippets"].append(snippet)
            if len(result["snippets"]) >= 3:
                break
    return results


def show_messages(
    connection: sqlite3.Connection,
    session_prefix: str,
    role: str | None = None,
    limit: int | None = None,
) -> list[dict[str, Any]]:
    """Return the message stream for all sessions matching a prefix."""

    if not session_prefix:
        return []
    params: list[Any] = [ _escape_like(session_prefix) + "%" ]
    where = ["s.session_id LIKE ? ESCAPE '\\'"]
    if role:
        where.append("m.role = ?")
        params.append(role)
    sql = f"""
        SELECT
            s.tool, s.session_id, s.title, s.cwd, s.created, s.updated,
            m.role, m.ts, m.text, m.id
        FROM sessions AS s
        JOIN messages AS m ON m.session_pk = s.id
        WHERE {" AND ".join(where)}
        ORDER BY s.tool, s.session_id, COALESCE(m.ts, 0), m.id
    """
    rows: list[dict[str, Any]] = []
    for row in connection.execute(sql, params):
        rows.append(
            {
                "tool": row[0],
                "session_id": row[1],
                "title": row[2] or "",
                "cwd": row[3] or "",
                "created": format_timestamp(row[4]),
                "updated": format_timestamp(row[5]),
                "role": row[6],
                "timestamp": format_timestamp(row[7]),
                "text": row[8],
            }
        )
        if limit is not None and len(rows) >= limit:
            break
    return rows
