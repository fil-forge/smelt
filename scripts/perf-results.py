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

    # The drill records its report's Sustained rates as sustained_* facts; a
    # run with no window of steady ingest has none, and its cells read "-".
    row = {
        **common_fields(run_dir, meta),
        "profile": drill.get("profile", suite.get("profile")),
        "stop_ingest_at": suite.get("stop_ingest_at"),
        "window_seconds": facts.get("window_seconds"),
        "offered_gbps": gb(facts.get("rate_target_bytes_per_second")),
        "drill_exit": suite.get("drill_exit"),
        "storage_qualification": suite.get("storage_qualification"),
        "windows": len(drill.get("windows") or []),
        "steady_windows": facts.get("sustained_windows", 0),
        "bytes_sent": facts.get("ingest_sent_bytes"),
        "bytes_read_back": drill["bytes_read_back"],
        "integrity_failures": drill["integrity_failures"],
        "requests": availability["requests"],
        "transport_errors": availability["transport_errors"],
        "status_408": availability["status_408"],
        "status_429": availability["status_429"],
        "status_5xx": availability["status_5xx"],
        "ingest_gbps_median": gb(facts.get("sustained_ingest_median_bytes_per_second")),
        "ingest_gbps_p5": gb(facts.get("sustained_ingest_p5_bytes_per_second")),
        "writes_per_second": facts.get("sustained_writes_median_per_second"),
        "read_back_gbps": gb(facts.get("sustained_read_median_bytes_per_second")),
        "restore_gbps": gb(facts.get("sustained_restore_median_bytes_per_second")),
        # Every setting the wrapper records, null where a run predates it.
        "settings": {key: suite.get(key) for key in DRILL_SETTINGS},
        "host": {"name": meta.get("host"), **meta.get("docker", {}), **meta.get("system", {})},
        "manifest": meta.get("manifest"),
        "piri": meta.get("piri"),
        "sprue": meta.get("sprue"),
    }
    return [row]


DRILL_SETTINGS = (
    "profile", "stop_ingest_at", "ramp", "window", "verify_lag_min", "verify_lag_max",
    "workers", "duration", "rate_target", "keep_objects", "enforce_floor", "progress",
    "accounts", "restore_scale", "config_note", "disk_factor",
)


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
        ("blob_put/s, median", lambda r: fmt_num(r["writes_per_second"], 1)),
        ("read-back GB/s, median", lambda r: fmt_num(r["read_back_gbps"], 3)),
        ("restore GB/s, median", lambda r: fmt_num(r["restore_gbps"], 3)),
        ("bytes sent (GB)", lambda r: fmt_num(gb(r["bytes_sent"]), 2)),
        ("integrity failures", lambda r: str(r["integrity_failures"])),
        ("requests", lambda r: str(r["requests"])),
        ("transport errors", lambda r: str(r["transport_errors"])),
        ("408 responses", lambda r: str(r["status_408"])),
        ("429 responses", lambda r: str(r["status_429"])),
        ("5xx responses", lambda r: str(r["status_5xx"])),
        ("windows (steady/all)", lambda r: f"{r['steady_windows']}/{r['windows']}"),
        ("drill exit", lambda r: str(r.get("drill_exit"))),
    ]
    lines = [["metric"] + labels]
    for name, cell in metrics:
        lines.append([name] + [cell(by_label[label]) for label in labels])
    print_grid(lines)
    print()
    print("rates are the drill report's Sustained rates; p5 is the highest ingest rate 95% of steady windows held")


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
        "images": {
            i.get("service"): {"ref": i.get("ref"), "digest": i.get("digest"), "revision": i.get("revision")}
            for i in meta.get("images") or []
        },
        "extra": meta.get("extra"),
    }


def print_run_headers(rows: list[dict], describe) -> list[str]:
    """Print one line per label (start time, suite settings, code versions)
    and return the labels in column order. A run on published images shows
    the images' revisions, since the sibling checkouts did not run."""
    labels = list(dict.fromkeys(r["label"] for r in rows))
    print()
    for label in labels:
        r = next(r for r in rows if r["label"] == label)
        repos = r.get("repos", {})
        revisions = {svc: i["revision"] for svc, i in sorted((r.get("images") or {}).items()) if i.get("revision")}
        if r.get("workspace_services") or not revisions:
            code = ", ".join(f"{name}@{fmt_git(info)}" for name, info in sorted(repos.items()) if info)
        else:
            code = ", ".join([f"smelt@{fmt_git(repos.get('smelt'))}"]
                             + [f"{svc}@{rev[:9]}" for svc, rev in revisions.items()])
        ws = ", ".join(r.get("workspace_services") or []) or "published images"
        print(f"{label}: {r['started_at_utc']}  {describe(r)}  [{code}]  workspace: {ws}")
    print("  (* = uncommitted changes)")
    print()
    return labels


def fmt_git(info: dict | None) -> str:
    if not info:
        return "?"
    # Nine characters: new runs record full SHAs, older ones short ones.
    return f"{info['sha'][:9]}{'*' if info.get('dirty') else ''}"


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
