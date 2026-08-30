"""Command-line interface for session-finder."""

from __future__ import annotations

import argparse
import json
import sys
from typing import Sequence

from .index import (
    format_timestamp,
    index_all,
    open_index,
    search_index,
    show_messages,
)


def build_parser() -> argparse.ArgumentParser:
    """Build the session-finder argument parser."""

    parser = argparse.ArgumentParser(
        prog="python3 -m session_finder",
        description="Search local AI sessions from opencode, Grok, Codex, Kimi Code, and Claude.",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    index_parser = subparsers.add_parser("index", help="build or update the local index")
    index_parser.add_argument("--full", action="store_true", help="rebuild the index from scratch")

    search_parser = subparsers.add_parser("search", help="search indexed session messages")
    search_parser.add_argument("query", help="search text")
    search_parser.add_argument("--tool", help="restrict to one tool")
    search_parser.add_argument("--cwd", help="restrict to sessions whose cwd contains this text")
    search_parser.add_argument("--after", help="only messages on or after YYYY-MM-DD")
    search_parser.add_argument("--limit", type=int, default=20, help="maximum sessions to show")
    search_parser.add_argument("--json", action="store_true", help="emit JSON")
    search_parser.add_argument(
        "--all",
        action="store_true",
        dest="include_system",
        help="include system/noise records (default hides them)",
    )

    show_parser = subparsers.add_parser("show", help="show a session message stream")
    show_parser.add_argument("session_id", help="full session ID or a unique prefix")
    show_parser.add_argument("--role", choices=("user", "assistant", "system"))
    show_parser.add_argument("--limit", type=int, help="maximum messages to show")

    return parser


def _print_index_summary(summary: dict[str, object]) -> None:
    """Print per-tool index counts and source processing details."""

    print(f"index: {summary['db_path']}")
    tools = summary["tools"]
    assert isinstance(tools, dict)
    for tool in ("opencode", "grok", "codex", "kimi-code", "claude"):
        counts = tools[tool]
        print(f"{tool}: sessions={counts['sessions']} messages={counts['messages']}")
    sources = summary["sources"]
    assert isinstance(sources, dict)
    print(
        "sources: "
        f"processed={sources['processed']} "
        f"unchanged={sources['unchanged']} "
        f"errors={sources['errors']}"
    )


def _print_search_results(query: str, results: list[dict[str, object]]) -> None:
    """Print human-readable aggregated search results."""

    print(f"search: {query!r} ({len(results)} sessions)")
    if not results:
        print("No matches.")
        return
    for number, result in enumerate(results, start=1):
        print(f"{number}. [{result['tool']}] {result['session_id']}")
        print(f"   title: {result['title'] or '-'}")
        print(f"   cwd: {result['cwd'] or '-'}")
        print(f"   time: {result['created']} .. {result['updated']}")
        print(f"   messages: {result['message_count']}")
        for path in result["source_paths"]:
            print(f"   path: {path}")
        for snippet in result["snippets"]:
            print(f"   snippet: {snippet}")


def _print_show(rows: list[dict[str, object]]) -> None:
    """Print a message stream with session metadata."""

    if not rows:
        print("No matching session.")
        return
    current: tuple[str, str] | None = None
    for row in rows:
        key = (str(row["tool"]), str(row["session_id"]))
        if key != current:
            if current is not None:
                print()
            print(f"=== [{row['tool']}] {row['session_id']} ===")
            print(f"title: {row['title'] or '-'}")
            print(f"cwd: {row['cwd'] or '-'}")
            current = key
        print(f"\n[{row['timestamp']}] {row['role']}")
        print(row["text"])


def main(argv: Sequence[str] | None = None) -> int:
    """Run the session-finder CLI."""

    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "index":
        summary = index_all(full=args.full)
        _print_index_summary(summary)
        return 0

    connection = open_index()
    try:
        if args.command == "search":
            try:
                results = search_index(
                    connection,
                    args.query,
                    tool=args.tool,
                    cwd=args.cwd,
                    after=args.after,
                    limit=args.limit,
                    include_system=args.include_system,
                )
            except ValueError as exc:
                parser.error(str(exc))
            if args.json:
                print(
                    json.dumps(
                        {"query": args.query, "count": len(results), "results": results},
                        ensure_ascii=False,
                        indent=2,
                    )
                )
            else:
                _print_search_results(args.query, results)
            return 0

        if args.command == "show":
            if args.limit is not None and args.limit <= 0:
                parser.error("--limit must be positive")
            rows = show_messages(connection, args.session_id, role=args.role, limit=args.limit)
            _print_show(rows)
            return 0
    finally:
        connection.close()
    return 0
