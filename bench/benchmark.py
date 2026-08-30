#!/usr/bin/env python3
"""Reproducible search and indexing benchmarks for session-finder.

The benchmark freezes the discovered source files into a temporary snapshot,
then runs all indexing experiments against that same snapshot.  Reports keep
only timings, counts, and SHA-256 fingerprints of results; message text,
session IDs, and source paths never leave the process.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import sqlite3
import statistics
import sys
import tempfile
import time
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator

if __package__ in {None, ""}:
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from session_finder import parsers
from session_finder.index import initialize_schema, index_all, open_index, search_index, show_messages
from session_finder.parsers import SourceSpec


DEFAULT_SEARCH_CASES = (
    {"name": "opencode", "query": "opencode"},
    {"name": "tavily", "query": "tavily"},
    {"name": "chinese", "query": "论文"},
    {"name": "multi_term", "query": "opencode 中转"},
    {"name": "underscore", "query": "tavily_mcp"},
)
DEFAULT_SHOW_CASES = (
    {"name": "prefix_broad", "session_prefix": "ses_", "limit": 100},
    {"name": "prefix_user", "session_prefix": "ses_", "role": "user", "limit": 100},
    {"name": "prefix_missing", "session_prefix": "__session_finder_bench_missing__", "limit": 100},
)


@dataclass(frozen=True)
class SourceSnapshot:
    """Frozen source specifications and their original-to-snapshot mapping."""

    specs: tuple[SourceSpec, ...]
    mapping: dict[str, str]
    digest: str
    file_count: int
    total_bytes: int


def _read_connection(db_path: Path) -> sqlite3.Connection:
    """Open an index snapshot without acquiring a write lock."""

    connection = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    return connection


def _backup_database(source_path: Path, target_path: Path) -> None:
    """Create a consistent SQLite snapshot outside the timed section."""

    source = sqlite3.connect(f"file:{source_path}?mode=ro", uri=True)
    target = sqlite3.connect(str(target_path))
    try:
        source.backup(target)
    finally:
        target.close()
        source.close()


def _file_fingerprint(path: Path) -> dict[str, Any]:
    """Return stable file metadata and a content digest."""

    stat = path.stat()
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return {
        "size": stat.st_size,
        "mtime_ns": stat.st_mtime_ns,
        "sha256": digest.hexdigest(),
    }


def _snapshot_path(root: Path, path: Path, home: Path) -> Path:
    """Map an absolute source path below home into a snapshot tree."""

    try:
        relative = path.relative_to(home)
    except ValueError:
        digest = hashlib.sha256(str(path).encode("utf-8")).hexdigest()
        relative = Path("external") / digest / path.name
    return root / "files" / relative


def _source_files(spec: SourceSpec) -> Iterator[Path]:
    """Yield source and auxiliary files included in a snapshot."""

    yield spec.path
    yield from spec.auxiliary_paths
    if spec.tool == "opencode":
        # SQLite creates and updates ``-shm`` as a derived WAL sidecar when a
        # read-only connection opens the snapshot.  Capture WAL contents, but
        # omit this transient shared-memory file from the frozen manifest.
        yield spec.path.with_name(spec.path.name + "-wal")


def _snapshot_from_manifest(root: Path, manifest: dict[str, Any]) -> SourceSnapshot:
    """Load and validate a previously created source snapshot."""

    files = manifest.get("files")
    sources = manifest.get("sources")
    if not isinstance(files, list) or not isinstance(sources, list):
        raise ValueError(f"invalid source snapshot manifest: {root / 'manifest.json'}")
    total_bytes = 0
    for entry in files:
        if not isinstance(entry, dict):
            raise ValueError("invalid source snapshot file entry")
        path = Path(str(entry["snapshot"]))
        actual = _file_fingerprint(path)
        expected = entry.get("fingerprint")
        if actual != expected:
            raise ValueError(f"source snapshot changed: {path}")
        total_bytes += int(actual["size"])

    specs: list[SourceSpec] = []
    mapping: dict[str, str] = {}
    for entry in sources:
        if not isinstance(entry, dict):
            raise ValueError("invalid source snapshot source entry")
        original = str(entry["path"])
        snapshot = str(entry["snapshot"])
        auxiliary_original = tuple(str(path) for path in entry.get("auxiliary", ()))
        auxiliary_snapshot = tuple(str(path) for path in entry.get("auxiliary_snapshot", ()))
        specs.append(
            SourceSpec(
                str(entry["tool"]),
                Path(snapshot),
                tuple(Path(path) for path in auxiliary_snapshot),
            )
        )
        mapping[original] = snapshot
        mapping.update(zip(auxiliary_original, auxiliary_snapshot))
    digest = _manifest_digest(manifest)
    return SourceSnapshot(tuple(specs), mapping, digest, len(files), total_bytes)


def _manifest_digest(value: dict[str, Any]) -> str:
    """Hash source identities and fingerprints, excluding snapshot locations."""

    canonical_sources: list[dict[str, Any]] = []
    for entry in value.get("sources", ()):
        if not isinstance(entry, dict):
            continue
        auxiliary = entry.get("auxiliary", ())
        if not isinstance(auxiliary, (list, tuple)):
            auxiliary = ()
        canonical_sources.append(
            {
                "tool": str(entry.get("tool", "")),
                "path": str(entry.get("path", "")),
                "auxiliary": [str(path) for path in auxiliary],
            }
        )

    canonical_files: list[dict[str, Any]] = []
    for entry in value.get("files", ()):
        if not isinstance(entry, dict):
            continue
        canonical_files.append(
            {
                "original": str(entry.get("original", "")),
                "fingerprint": entry.get("fingerprint"),
            }
        )

    canonical = {
        "sources": sorted(
            canonical_sources,
            key=lambda item: (item["tool"], item["path"], item["auxiliary"]),
        ),
        "files": sorted(canonical_files, key=lambda item: item["original"]),
    }
    encoded = json.dumps(
        canonical, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def _create_source_snapshot(root: Path) -> SourceSnapshot:
    """Copy all discovered source files and write a reusable manifest."""

    root.mkdir(parents=True, exist_ok=True)
    home = Path.home().resolve()
    original_specs = tuple(parsers.iter_source_specs())
    files: dict[str, dict[str, Any]] = {}
    source_entries: list[dict[str, Any]] = []

    for spec in original_specs:
        snapshot_path = _snapshot_path(root, spec.path.resolve(), home)
        auxiliary_snapshot: list[str] = []
        for auxiliary in spec.auxiliary_paths:
            auxiliary_path = _snapshot_path(root, auxiliary.resolve(), home)
            auxiliary_snapshot.append(str(auxiliary_path))
        source_entries.append(
            {
                "tool": spec.tool,
                "path": str(spec.path.resolve()),
                "snapshot": str(snapshot_path),
                "auxiliary": [str(path.resolve()) for path in spec.auxiliary_paths],
                "auxiliary_snapshot": auxiliary_snapshot,
            }
        )

        for source_path in _source_files(spec):
            source_path = source_path.resolve()
            if not source_path.is_file() or str(source_path) in files:
                continue
            before = _file_fingerprint(source_path)
            target = _snapshot_path(root, source_path, home)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source_path, target)
            after = _file_fingerprint(source_path)
            if before != after:
                raise RuntimeError(f"source changed while snapshotting: {source_path}")
            files[str(source_path)] = {
                "original": str(source_path),
                "snapshot": str(target),
                "fingerprint": _file_fingerprint(target),
            }

    manifest_body = {
        "format": 1,
        "sources": source_entries,
        "files": [files[path] for path in sorted(files)],
    }
    manifest = dict(manifest_body)
    manifest["digest"] = _manifest_digest(manifest_body)
    (root / "manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    return _snapshot_from_manifest(root, manifest)


def _prepare_source_snapshot(root: Path) -> SourceSnapshot:
    """Create or reuse a validated source snapshot directory."""

    manifest_path = root / "manifest.json"
    if manifest_path.is_file():
        return _snapshot_from_manifest(
            root, json.loads(manifest_path.read_text(encoding="utf-8"))
        )
    if root.exists() and any(root.iterdir()):
        raise ValueError(f"snapshot directory is not empty: {root}")
    return _create_source_snapshot(root)


@contextmanager
def _use_source_snapshot(snapshot: SourceSnapshot) -> Iterator[None]:
    """Temporarily make index_all discover the frozen source specifications."""

    original_iterator = parsers.iter_source_specs
    parsers.iter_source_specs = lambda: iter(snapshot.specs)  # type: ignore[assignment]
    try:
        yield
    finally:
        parsers.iter_source_specs = original_iterator  # type: ignore[assignment]


def _retarget_index(connection: sqlite3.Connection, mapping: dict[str, str]) -> None:
    """Retarget a copied index to snapshot paths and remove stale sources."""

    for original, snapshot in mapping.items():
        connection.execute(
            "UPDATE sessions SET source_path = ? WHERE source_path = ?",
            (snapshot, original),
        )
        connection.execute(
            "UPDATE sources SET source_path = ? WHERE source_path = ?",
            (snapshot, original),
        )
    paths = tuple(mapping.values())
    if paths:
        placeholders = ",".join("?" for _ in paths)
        connection.execute(
            f"DELETE FROM sessions WHERE source_path NOT IN ({placeholders})", paths
        )
        connection.execute(
            f"DELETE FROM sources WHERE source_path NOT IN ({placeholders})", paths
        )


def _percentile(values: list[float], percentile: float) -> float:
    """Return a linear-interpolation percentile for a non-empty sample."""

    if not values:
        raise ValueError("values must not be empty")
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    position = (len(ordered) - 1) * percentile
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    fraction = position - lower
    return ordered[lower] + (ordered[upper] - ordered[lower]) * fraction


def _timing_summary(durations: list[float]) -> dict[str, float] | None:
    """Summarize durations in milliseconds, or return ``None`` when skipped."""

    if not durations:
        return None
    milliseconds = [duration * 1000.0 for duration in durations]
    return {
        "min_ms": min(milliseconds),
        "median_ms": statistics.median(milliseconds),
        "p95_ms": _percentile(milliseconds, 0.95),
        "max_ms": max(milliseconds),
    }


def _result_digest(results: list[dict[str, Any]]) -> str:
    """Return a stable digest without writing private result payloads."""

    encoded = json.dumps(results, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def _result_summary(
    results: list[dict[str, Any]],
    query: str,
    snapshot_to_original: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Summarize results after normalizing temporary source paths."""

    if snapshot_to_original:
        normalized: list[dict[str, Any]] = []
        for result in results:
            source_paths = result.get("source_paths")
            if not isinstance(source_paths, list):
                normalized.append(result)
                continue
            normalized_result = dict(result)
            normalized_result["source_paths"] = [
                snapshot_to_original.get(str(path), str(path))
                for path in source_paths
            ]
            normalized.append(normalized_result)
        results = normalized
    return {"query": query, "count": len(results), "sha256": _result_digest(results)}


def _show_summary(results: list[dict[str, Any]], case: dict[str, Any]) -> dict[str, Any]:
    """Summarize a show result list for a privacy-preserving report."""

    definition = {
        key: case[key]
        for key in ("session_prefix", "role", "limit")
        if key in case
    }
    return {
        "case": definition,
        "count": len(results),
        "sha256": _result_digest(results),
    }


def _search_snapshot(
    db_path: Path,
    cases: tuple[dict[str, str], ...],
    repeats: int,
    snapshot_to_original: dict[str, str] | None = None,
) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """Measure fixed searches and return timings plus result fingerprints."""

    timings: dict[str, list[float]] = {case["name"]: [] for case in cases}
    results: dict[str, dict[str, Any]] = {}
    connection = _read_connection(db_path)
    try:
        for case in cases:
            name = case["name"]
            query = case["query"]
            search_index(connection, query)
            value: list[dict[str, Any]] = []
            for _ in range(repeats):
                started = time.perf_counter()
                value = search_index(connection, query)
                timings[name].append(time.perf_counter() - started)
            results[name] = _result_summary(value, query, snapshot_to_original)
    finally:
        connection.close()
    return (
        {
            name: {
                "query": case["query"],
                "repeats": repeats,
                "timing": _timing_summary(timings[name]),
                "durations_ms": [duration * 1000.0 for duration in timings[name]],
            }
            for name, case in ((case["name"], case) for case in cases)
        },
        results,
    )


def _show_snapshot(
    db_path: Path,
    cases: tuple[dict[str, Any], ...],
    repeats: int,
) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """Measure fixed show cases and return timings plus result fingerprints."""

    timings: dict[str, list[float]] = {case["name"]: [] for case in cases}
    results: dict[str, dict[str, Any]] = {}
    connection = _read_connection(db_path)
    try:
        for case in cases:
            name = case["name"]
            kwargs = {
                key: case[key]
                for key in ("role", "limit")
                if key in case
            }
            show_messages(connection, case["session_prefix"], **kwargs)
            value: list[dict[str, Any]] = []
            for _ in range(repeats):
                started = time.perf_counter()
                value = show_messages(connection, case["session_prefix"], **kwargs)
                timings[name].append(time.perf_counter() - started)
            results[name] = _show_summary(value, case)
    finally:
        connection.close()
    return (
        {
            name: {
                "case": {
                    key: case[key]
                    for key in ("session_prefix", "role", "limit")
                    if key in case
                },
                "repeats": repeats,
                "timing": _timing_summary(timings[name]),
                "durations_ms": [duration * 1000.0 for duration in timings[name]],
            }
            for name, case in ((case["name"], case) for case in cases)
        },
        results,
    )


def _sanitize_index_summary(summary: dict[str, Any]) -> dict[str, Any]:
    """Remove temporary absolute paths from index_all's public summary."""

    return {
        "tools": summary.get("tools", {}),
        "sources": summary.get("sources", {}),
    }


def _run_indexing(
    source_db: Path,
    snapshot: SourceSnapshot,
    cases: tuple[dict[str, str], ...],
    show_cases: tuple[dict[str, Any], ...],
    repeats: int,
    full: bool,
    temporary_directory: Path,
    search_repeats: int,
) -> tuple[list[float], list[dict[str, Any]], list[dict[str, dict[str, Any]]], list[dict[str, dict[str, Any]]]]:
    """Run indexing experiments, excluding snapshot/setup work from timings."""

    durations: list[float] = []
    summaries: list[dict[str, Any]] = []
    search_results: list[dict[str, dict[str, Any]]] = []
    show_results: list[dict[str, dict[str, Any]]] = []
    snapshot_to_original = {
        snapshot_path: original_path
        for original_path, snapshot_path in snapshot.mapping.items()
    }
    for number in range(repeats):
        target = temporary_directory / (
            f"full-{number}.db" if full else f"incremental-{number}.db"
        )
        if not full:
            _backup_database(source_db, target)
            connection = open_index(target)
            _retarget_index(connection, snapshot.mapping)
            connection.commit()
            initialize_schema(connection)
            connection.close()
        started = time.perf_counter()
        summary = index_all(full=full, db_path=target)
        durations.append(time.perf_counter() - started)
        summaries.append(_sanitize_index_summary(summary))
        _, fingerprints = _search_snapshot(
            target, cases, search_repeats, snapshot_to_original
        )
        _, show_fingerprints = _show_snapshot(target, show_cases, search_repeats)
        search_results.append(fingerprints)
        show_results.append(show_fingerprints)
    return durations, summaries, search_results, show_results


def _parse_cases(raw_cases: list[str] | None) -> tuple[dict[str, str], ...]:
    """Parse optional ``name=query`` overrides from the command line."""

    if not raw_cases:
        return DEFAULT_SEARCH_CASES
    cases: list[dict[str, str]] = []
    names: set[str] = set()
    for raw in raw_cases:
        name, separator, query = raw.partition("=")
        if not separator or not name or not query:
            raise ValueError(f"search case must be name=query: {raw!r}")
        if name in names:
            raise ValueError(f"duplicate search case name: {name!r}")
        names.add(name)
        cases.append({"name": name, "query": query})
    return tuple(cases)


def _sanitize_report_path(path: Path) -> str:
    """Return a non-sensitive database label for a report."""

    return path.name


def _run_benchmark(
    db_path: Path,
    snapshot: SourceSnapshot,
    cases: tuple[dict[str, str], ...],
    show_cases: tuple[dict[str, Any], ...],
    search_repeats: int,
    show_repeats: int,
    incremental_repeats: int,
    full_repeats: int,
) -> dict[str, Any]:
    """Run all requested experiments and return a JSON-serializable report."""

    started = time.perf_counter()
    with tempfile.TemporaryDirectory(prefix="session-finder-bench-", dir=str(db_path.parent)) as directory:
        temporary_directory = Path(directory)
        with _use_source_snapshot(snapshot):
            search_metrics, search_results = _search_snapshot(
                db_path, cases, search_repeats
            )
            show_metrics, show_results = _show_snapshot(
                db_path, show_cases, show_repeats
            )
            incremental = _run_indexing(
                db_path,
                snapshot,
                cases,
                show_cases,
                incremental_repeats,
                False,
                temporary_directory,
                search_repeats,
            )
            full = _run_indexing(
                db_path,
                snapshot,
                cases,
                show_cases,
                full_repeats,
                True,
                temporary_directory,
                search_repeats,
            )
    return {
        "format": 2,
        "db": {
            "label": _sanitize_report_path(db_path),
            "size_bytes": db_path.stat().st_size,
        },
        "environment": {
            "platform": os.uname().sysname,
            "python": sys.version.split()[0],
            "sqlite": sqlite3.sqlite_version,
        },
        "source_snapshot": {
            "digest": snapshot.digest,
            "files": snapshot.file_count,
            "bytes": snapshot.total_bytes,
        },
        "search": search_metrics,
        "search_results": search_results,
        "show": show_metrics,
        "show_results": show_results,
        "incremental": {
            "repeats": incremental_repeats,
            "durations_s": incremental[0],
            "timing": _timing_summary(incremental[0]),
            "summaries": incremental[1],
            "search_results": incremental[2],
            "show_results": incremental[3],
        },
        "full_rebuild": {
            "repeats": full_repeats,
            "durations_s": full[0],
            "timing": _timing_summary(full[0]),
            "summaries": full[1],
            "search_results": full[2],
            "show_results": full[3],
        },
        "wall_time_s": time.perf_counter() - started,
    }


def _coerce_result_summary(value: Any, query: str | None = None) -> dict[str, Any] | None:
    """Convert old raw-result reports and new fingerprint reports alike."""

    if not isinstance(value, dict):
        return None
    if isinstance(value.get("results"), list):
        results = value["results"]
        return _result_summary(results, str(value.get("query", query or "")))
    if "sha256" in value and "count" in value:
        return {
            "query": str(value.get("query", query or "")),
            "count": int(value["count"]),
            "sha256": str(value["sha256"]),
        }
    return None


def _compare_result_maps(
    failures: list[str],
    label: str,
    expected: Any,
    actual: Any,
) -> None:
    """Compare privacy-preserving result summaries and record differences."""

    if not isinstance(expected, dict) or not isinstance(actual, dict):
        failures.append(f"{label}: result summaries missing")
        return
    if expected.keys() != actual.keys():
        failures.append(
            f"{label}: case names differ "
            f"(baseline={sorted(expected)}, current={sorted(actual)})"
        )
    for name in sorted(expected.keys() & actual.keys()):
        expected_summary = _coerce_result_summary(expected[name])
        actual_summary = _coerce_result_summary(actual[name])
        if expected_summary is None or actual_summary is None:
            failures.append(f"{label}/{name}: malformed result summary")
            continue
        if expected_summary["query"] != actual_summary["query"]:
            failures.append(f"{label}/{name}: query definition differs")
        if expected_summary["count"] != actual_summary["count"]:
            failures.append(
                f"{label}/{name}: count differs "
                f"(baseline={expected_summary['count']}, current={actual_summary['count']})"
            )
        if expected_summary["sha256"] != actual_summary["sha256"]:
            failures.append(f"{label}/{name}: result fingerprint differs")


def _compare_result_lists(
    failures: list[str],
    label: str,
    expected: Any,
    actual: Any,
) -> None:
    """Compare per-target result summary lists."""

    if not isinstance(expected, list) or not isinstance(actual, list):
        failures.append(f"{label}: target result summaries missing")
        return
    if len(expected) != len(actual):
        failures.append(f"{label}: repeat count differs")
    for number, (expected_item, actual_item) in enumerate(zip(expected, actual)):
        _compare_result_maps(failures, f"{label}[{number}]", expected_item, actual_item)


def _compare_results(baseline: dict[str, Any], current: dict[str, Any]) -> list[str]:
    """Return result-set and query-definition parity failures."""

    failures: list[str] = []
    if baseline.get("source_snapshot", {}).get("digest") != current.get("source_snapshot", {}).get("digest"):
        failures.append("source snapshot digest differs")

    baseline_full = baseline.get("full_rebuild", {})
    current_full = current.get("full_rebuild", {})
    baseline_incremental = baseline.get("incremental", {})
    current_incremental = current.get("incremental", {})
    if current_full.get("repeats", 0):
        _compare_result_lists(
            failures,
            "full/search",
            baseline_full.get("search_results"),
            current_full.get("search_results"),
        )
        _compare_result_lists(
            failures,
            "full/show",
            baseline_full.get("show_results"),
            current_full.get("show_results"),
        )
    if current_incremental.get("repeats", 0):
        _compare_result_lists(
            failures,
            "incremental/search",
            baseline_incremental.get("search_results"),
            current_incremental.get("search_results"),
        )
        _compare_result_lists(
            failures,
            "incremental/show",
            baseline_incremental.get("show_results"),
            current_incremental.get("show_results"),
        )

    # Legacy reports had only raw searches against the source index. Keep a
    # compatibility fallback so old baseline files can still be checked.
    if not baseline_full.get("search_results") and not baseline_incremental.get("search_results"):
        _compare_result_maps(
            failures,
            "source/search",
            baseline.get("search_results"),
            current.get("search_results"),
        )
    return failures


def main() -> int:
    """Run benchmarks and optionally compare result fingerprints with a baseline."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", type=Path, default=Path.home() / ".cache/session-finder/index.db")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--compare", type=Path)
    parser.add_argument("--snapshot-dir", type=Path)
    parser.add_argument("--search-repeats", type=int, default=7)
    parser.add_argument("--show-repeats", type=int, default=3)
    parser.add_argument("--incremental-repeats", type=int, default=2)
    parser.add_argument("--full-repeats", type=int, default=1)
    parser.add_argument("--skip-full", action="store_true")
    parser.add_argument(
        "--case",
        action="append",
        dest="cases",
        help="override a search case (repeatable NAME=QUERY)",
    )
    args = parser.parse_args()
    if not args.db.is_file():
        parser.error(f"database does not exist: {args.db}")
    if min(args.search_repeats, args.show_repeats, args.incremental_repeats) <= 0:
        parser.error("repeat counts must be positive")
    if args.full_repeats <= 0 and not args.skip_full:
        parser.error("full repeat count must be positive")
    try:
        cases = _parse_cases(args.cases)
    except ValueError as exc:
        parser.error(str(exc))

    snapshot_temporary: tempfile.TemporaryDirectory[str] | None = None
    snapshot_dir = args.snapshot_dir
    if snapshot_dir is None:
        snapshot_temporary = tempfile.TemporaryDirectory(
            prefix="session-finder-sources-", dir=str(args.db.parent)
        )
        snapshot_dir = Path(snapshot_temporary.name)
    try:
        try:
            snapshot = _prepare_source_snapshot(snapshot_dir)
        except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
            parser.error(str(exc))
        full_repeats = 0 if args.skip_full else args.full_repeats
        report = _run_benchmark(
            args.db,
            snapshot,
            cases,
            DEFAULT_SHOW_CASES,
            args.search_repeats,
            args.show_repeats,
            args.incremental_repeats,
            full_repeats,
        )
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(
            json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        print(
            json.dumps(
                {
                    "output": str(args.output),
                    "snapshot": report["source_snapshot"],
                    "search": {
                        name: metrics["timing"]
                        for name, metrics in report["search"].items()
                    },
                    "show": {
                        name: metrics["timing"]
                        for name, metrics in report["show"].items()
                    },
                    "incremental": report["incremental"]["timing"],
                    "full_rebuild": report["full_rebuild"]["timing"],
                },
                ensure_ascii=False,
                indent=2,
            )
        )

        if args.compare:
            try:
                baseline = json.loads(args.compare.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                parser.error(f"invalid baseline report: {exc}")
            failures = _compare_results(baseline, report)
            if failures:
                print("search/show parity: FAIL")
                for failure in failures:
                    print(f"- {failure}")
                return 1
            print("search/show parity: PASS")
        return 0
    finally:
        if snapshot_temporary is not None:
            snapshot_temporary.cleanup()


if __name__ == "__main__":
    raise SystemExit(main())
