#!/usr/bin/env python3
"""Record and compare perf runs.

    perf-results.py record  <suite> <run-dir>       # append the run's results to runs.jsonl
    perf-results.py compare <suite> [label ...]     # side-by-side table; no labels = all

Each suite keeps one append-only generated/perf-runs/<suite>/runs.jsonl with
one row per (run, operation, file size). `record` builds those rows from the
run directory's metadata.json and the s3-speedtests summary files; `compare`
pivots them into a table with labels as columns. When a label was run more
than once, the latest run wins.
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
    if argv[1] == "record":
        if len(argv) != 4:
            print("usage: perf-results.py record <suite> <run-dir>", file=sys.stderr)
            return 2
        rows = record(suite, Path(argv[3]))
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


def record(suite: str, run_dir: Path) -> list[dict]:
    meta = json.loads((run_dir / "metadata.json").read_text())
    rows: list[dict] = []
    for summary_path in sorted((run_dir / "speedtest").glob("s3_*_speedtest_summary_*.jsonl")):
        for line in summary_path.read_text().splitlines():
            if not line.strip():
                continue
            s = json.loads(line)
            if s.get("record_type") not in ("s3_upload_summary", "s3_download_summary"):
                continue
            rows.append({
                "label": meta["label"],
                "started_at_utc": meta["started_at_utc"],
                "run_dir": str(run_dir.relative_to(PROJECT)) if run_dir.is_relative_to(PROJECT) else str(run_dir),
                "repos": meta.get("repos", {}),
                "workspace_services": meta.get("workspace_services", []),
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


def print_table(rows: list[dict]) -> None:
    labels = list(dict.fromkeys(r["label"] for r in rows))
    print()
    for label in labels:
        r = next(r for r in rows if r["label"] == label)
        repos = ", ".join(
            f"{name}@{info['sha']}{'*' if info.get('dirty') else ''}"
            for name, info in sorted(r.get("repos", {}).items()) if info
        )
        ws = ", ".join(r.get("workspace_services") or []) or "published images"
        print(f"{label}: {r['started_at_utc']}  file set {r.get('file_set')}  [{repos}]  workspace: {ws}")
    print("  (* = uncommitted changes)")
    print()

    cells: dict[tuple[str, float], dict[str, dict]] = {}
    for r in rows:
        cells.setdefault((r["operation"], r["file_size_mib"]), {})[r["label"]] = r
    header = ["operation", "size (MiB)"] + labels
    lines = [header]
    for (op, size), by_label in sorted(cells.items()):
        line = [op, fmt_size(size)]
        for label in labels:
            r = by_label.get(label)
            line.append(fmt_cell(r) if r else "-")
        lines.append(line)
    widths = [max(len(row[i]) for row in lines) for i in range(len(header))]
    for i, row in enumerate(lines):
        print("  ".join(cell.ljust(widths[j]) for j, cell in enumerate(row)))
        if i == 0:
            print("  ".join("-" * w for w in widths))
    print()
    print("cells: median Mbps [min-max]  median seconds  (ok/attempts)")


def fmt_size(mib: float) -> str:
    return f"{mib:g}"


def fmt_cell(r: dict) -> str:
    med = r.get("median_throughput_mbps")
    if med is None:
        return f"FAILED ({r.get('successes', 0)}/{r.get('attempts', '?')})"
    spread = ""
    if r.get("min_throughput_mbps") is not None and r.get("max_throughput_mbps") != r.get("min_throughput_mbps"):
        spread = f" [{r['min_throughput_mbps']:g}-{r['max_throughput_mbps']:g}]"
    return f"{med:g} Mbps{spread}  {r.get('median_elapsed_seconds', 0):g}s  ({r.get('successes')}/{r.get('attempts')})"


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
