#!/usr/bin/env python3
"""Record and compare perf runs.

    perf-results.py record  <suite> <run-dir>       # append the run's results to runs.jsonl
    perf-results.py compare <suite> [label ...]     # side-by-side table; no labels = all

Suites: s3-speedtest, drill.

Each suite keeps one append-only generated/perf-runs/<suite>/runs.jsonl.
`record` builds rows from the run directory's metadata.json and the suite's
own output: one row per (operation, file size) for s3-speedtest, one row per
run for drill. `compare` pivots them into a table with labels as columns.
When a label was run more than once, the latest run wins.
"""

from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path

PROJECT = Path(__file__).resolve().parent.parent
RUNS_ROOT = PROJECT / "generated" / "perf-runs"


def main(argv: list[str]) -> int:
    if len(argv) < 3 or argv[1] not in ("record", "compare"):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    suite = argv[2]
    if suite not in SUITES:
        print(f"unknown suite {suite!r}; known suites: {', '.join(sorted(SUITES))}", file=sys.stderr)
        return 2
    build_rows, print_table = SUITES[suite]
    if argv[1] == "record":
        if len(argv) != 4:
            print("usage: perf-results.py record <suite> <run-dir>", file=sys.stderr)
            return 2
        rows = record(suite, Path(argv[3]), build_rows)
        print_table(rows)
        return 0
    labels = argv[3:]
    rows = load_rows(suite, labels)
    if not rows:
        print(f"no runs recorded for suite {suite!r}", file=sys.stderr)
        return 1
    # A label with no run (say, the `after` run failed before recording) must
    # not quietly turn a comparison into a one-column table.
    missing = [label for label in labels if not any(r["label"] == label for r in rows)]
    if missing:
        print(f"no runs recorded for suite {suite!r} with label(s): {', '.join(missing)}", file=sys.stderr)
        return 1
    print_table(rows)
    return 0


def record(suite: str, run_dir: Path, build_rows) -> list[dict]:
    meta = json.loads((run_dir / "metadata.json").read_text())
    rows = build_rows(run_dir, meta)
    runs_file = RUNS_ROOT / suite / "runs.jsonl"
    runs_file.parent.mkdir(parents=True, exist_ok=True)
    with runs_file.open("a", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, sort_keys=True) + "\n")
    print(f"recorded {len(rows)} row(s) to {runs_file}")
    return rows


def load_rows(suite: str, labels: list[str]) -> list[dict]:
    runs_file = RUNS_ROOT / suite / "runs.jsonl"
    if not runs_file.exists():
        return []
    rows = [json.loads(line) for line in runs_file.read_text().splitlines() if line.strip()]
    if labels:
        rows = [r for r in rows if r["label"] in labels]
    # Latest run per label wins.
    latest_start: dict[str, str] = {}
    for r in rows:
        latest_start[r["label"]] = max(latest_start.get(r["label"], ""), r["started_at_utc"])
    rows = [r for r in rows if r["started_at_utc"] == latest_start[r["label"]]]
    if labels:
        order = {label: i for i, label in enumerate(labels)}
        rows.sort(key=lambda r: order[r["label"]])
    else:
        rows.sort(key=lambda r: r["started_at_utc"])
    return rows


# --- s3-speedtest -----------------------------------------------------------


def speedtest_rows(run_dir: Path, meta: dict) -> list[dict]:
    rows: list[dict] = []
    for summary_path in sorted((run_dir / "speedtest").glob("s3_*_speedtest_summary_*.jsonl")):
        for line in summary_path.read_text().splitlines():
            if not line.strip():
                continue
            s = json.loads(line)
            if s.get("record_type") not in ("s3_upload_summary", "s3_download_summary"):
                continue
            rows.append({
                **common_fields(run_dir, meta),
                "file_set": meta.get("suite", {}).get("file_set"),
                "operation": s["operation"],
                "file_size_mib": s["file_size_mib"],
                "attempts": s.get("attempt_count"),
                "successes": s.get("success_count"),
                "failures": s.get("failure_count"),
                "median_throughput_mbps": s.get("median_throughput_mbps"),
                "min_throughput_mbps": s.get("min_throughput_mbps"),
                "max_throughput_mbps": s.get("max_throughput_mbps"),
                "median_elapsed_seconds": s.get("median_elapsed_seconds"),
            })
    if not rows:
        raise SystemExit(f"no s3_*_speedtest_summary_*.jsonl under {run_dir / 'speedtest'}")
    return rows


def print_speedtest_table(rows: list[dict]) -> None:
    labels = print_run_headers(rows, lambda r: f"file set {r.get('file_set')}")

    cells: dict[tuple[str, float], dict[str, dict]] = {}
    for r in rows:
        cells.setdefault((r["operation"], r["file_size_mib"]), {})[r["label"]] = r
    lines = [["operation", "size (MiB)"] + labels]
    for (op, size), by_label in sorted(cells.items()):
        line = [op, fmt_size(size)]
        for label in labels:
            r = by_label.get(label)
            line.append(fmt_speedtest_cell(r) if r else "-")
        lines.append(line)
    print_grid(lines)
    print()
    print("cells: median Mbps [min-max]  median seconds  (ok/attempts)")


def fmt_size(mib: float) -> str:
    return f"{mib:g}"


def fmt_speedtest_cell(r: dict) -> str:
    med = r.get("median_throughput_mbps")
    if med is None:
        return f"FAILED ({r.get('successes', 0)}/{r.get('attempts', '?')})"
    spread = ""
    if r.get("min_throughput_mbps") is not None and r.get("max_throughput_mbps") != r.get("min_throughput_mbps"):
        spread = f" [{r['min_throughput_mbps']:g}-{r['max_throughput_mbps']:g}]"
    return f"{med:g} Mbps{spread}  {r.get('median_elapsed_seconds', 0):g}s  ({r.get('successes')}/{r.get('attempts')})"


# --- drill ------------------------------------------------------------------


def drill_rows(run_dir: Path, meta: dict) -> list[dict]:
    evidence_dir = run_dir / "drill" / "evidence"
    evidence_files = sorted(evidence_dir.glob("drill-*.json"))
    if len(evidence_files) != 1:
        raise SystemExit(f"expected one drill-*.json under {evidence_dir}, found {len(evidence_files)}")
    drill = json.loads(evidence_files[0].read_text())["drill"]
    suite = meta.get("suite", {})
    facts = drill.get("facts") or {}
    availability = drill["availability"]
    windows = drill.get("windows") or []
    # A drill that keeps measuring after the cap would end on windows with no
    # ingest (read-back draining). They say nothing about ingest speed; a
    # stall in the middle of the run does, so only the tail is dropped.
    ingest_windows = list(windows)
    while ingest_windows and not ingest_windows[-1]["ingest_bytes"]:
        ingest_windows.pop()

    row = {
        **common_fields(run_dir, meta),
        "profile": drill.get("profile", suite.get("profile")),
        "stop_ingest_at": suite.get("stop_ingest_at"),
        "window_seconds": facts.get("window_seconds"),
        "offered_gbps": gb(facts.get("rate_target_bytes_per_second")),
        "drill_exit": suite.get("drill_exit"),
        "storage_qualification": suite.get("storage_qualification"),
        "windows": len(windows),
        "ingest_windows": len(ingest_windows),
        "bytes_sent": drill["bytes_ingested"] + facts.get("aggregate_bytes", 0),
        "bytes_read_back": drill["bytes_read_back"],
        "integrity_failures": drill["integrity_failures"],
        "requests": availability["requests"],
        "transport_errors": availability["transport_errors"],
        "status_408": availability["status_408"],
        "status_429": availability["status_429"],
        "status_5xx": availability["status_5xx"],
        "ingest_gbps_median": None,
        "ingest_gbps_p5": None,
        "writes_per_second": None,
        "read_back_gbps": None,
        "restore_gbps": None,
    }
    if ingest_windows:
        rates = sorted(gb(w["ingest_bytes_per_second"]) for w in ingest_windows)
        ingest_seconds = sum(w["seconds"] for w in ingest_windows)
        row["ingest_gbps_median"] = statistics.median(rates)
        row["ingest_gbps_p5"] = rates[p5_index(len(rates))]
        row["writes_per_second"] = sum((w.get("requests") or {}).get("blob_put", 0) for w in ingest_windows) / ingest_seconds
    if windows:
        seconds = sum(w["seconds"] for w in windows)
        row["read_back_gbps"] = gb(sum(w["read_bytes"] for w in windows) / seconds)
        row["restore_gbps"] = gb(sum(w["restore_bytes"] for w in windows) / seconds)
    return [row]


def p5_index(n: int) -> int:
    """Index into n ascending window rates of the highest rate that at least
    95% of the windows reach, by the drill's own rule (MeetsTarget: windows at
    or above the target >= 0.95 * windows, in float64). With fewer than 20
    windows this is the slowest window."""
    return max(k for k in range(n) if (n - k) >= 0.95 * n)


def gb(value: float | None) -> float | None:
    return None if value is None else value / 1e9


def print_drill_table(rows: list[dict]) -> None:
    # p5 depends on the window length (short windows show dips that long ones
    # average out), so the header shows it for every run.
    labels = print_run_headers(rows, lambda r: (
        f"profile {r.get('profile')}  cap {r.get('stop_ingest_at')}  window {fmt_seconds(r.get('window_seconds'))}"
        f"  offered {fmt_num(r.get('offered_gbps'), 2)} GB/s"
        f"  storage-qualification@{fmt_git(r.get('storage_qualification'))}"
    ))
    by_label = {r["label"]: r for r in rows}
    metrics = [
        ("ingest GB/s, median", lambda r: fmt_num(r["ingest_gbps_median"], 3)),
        ("ingest GB/s, p5", lambda r: fmt_num(r["ingest_gbps_p5"], 3)),
        ("blob_put/s", lambda r: fmt_num(r["writes_per_second"], 1)),
        ("read-back GB/s", lambda r: fmt_num(r["read_back_gbps"], 3)),
        ("restore GB/s", lambda r: fmt_num(r["restore_gbps"], 3)),
        ("bytes sent (GB)", lambda r: fmt_num(gb(r["bytes_sent"]), 2)),
        ("integrity failures", lambda r: str(r["integrity_failures"])),
        ("requests", lambda r: str(r["requests"])),
        ("transport errors", lambda r: str(r["transport_errors"])),
        ("408 responses", lambda r: str(r["status_408"])),
        ("429 responses", lambda r: str(r["status_429"])),
        ("5xx responses", lambda r: str(r["status_5xx"])),
        ("windows (ingest/all)", lambda r: f"{r['ingest_windows']}/{r['windows']}"),
        ("drill exit", lambda r: str(r.get("drill_exit"))),
    ]
    lines = [["metric"] + labels]
    for name, cell in metrics:
        lines.append([name] + [cell(by_label[label]) for label in labels])
    print_grid(lines)
    print()
    print("p5: the highest rate at least 95% of ingest windows reached (the drill's target rule)")


def fmt_num(value: float | None, digits: int) -> str:
    return "-" if value is None else f"{value:.{digits}f}"


def fmt_seconds(value: float | None) -> str:
    return "?" if value is None else f"{value:g}s"


# --- shared -----------------------------------------------------------------


def common_fields(run_dir: Path, meta: dict) -> dict:
    return {
        "label": meta["label"],
        "started_at_utc": meta["started_at_utc"],
        "run_dir": str(run_dir.relative_to(PROJECT)) if run_dir.is_relative_to(PROJECT) else str(run_dir),
        "repos": meta.get("repos", {}),
        "workspace_services": meta.get("workspace_services", []),
    }


def print_run_headers(rows: list[dict], describe) -> list[str]:
    """Print one line per label (start time, suite settings, code versions)
    and return the labels in column order."""
    labels = list(dict.fromkeys(r["label"] for r in rows))
    print()
    for label in labels:
        r = next(r for r in rows if r["label"] == label)
        repos = ", ".join(
            f"{name}@{fmt_git(info)}" for name, info in sorted(r.get("repos", {}).items()) if info
        )
        ws = ", ".join(r.get("workspace_services") or []) or "published images"
        print(f"{label}: {r['started_at_utc']}  {describe(r)}  [{repos}]  workspace: {ws}")
    print("  (* = uncommitted changes)")
    print()
    return labels


def fmt_git(info: dict | None) -> str:
    if not info:
        return "?"
    return f"{info['sha']}{'*' if info.get('dirty') else ''}"


def print_grid(lines: list[list[str]]) -> None:
    widths = [max(len(row[i]) for row in lines) for i in range(len(lines[0]))]
    for i, row in enumerate(lines):
        print("  ".join(cell.ljust(widths[j]) for j, cell in enumerate(row)))
        if i == 0:
            print("  ".join("-" * w for w in widths))


SUITES = {
    "s3-speedtest": (speedtest_rows, print_speedtest_table),
    "drill": (drill_rows, print_drill_table),
}


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
