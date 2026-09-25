# Performance Testing Against the Local Stack

Smelt can serve as the target of a performance test loop: run a benchmark
against the local ingot, change ingot, sprue or piri in your checkouts,
redeploy only what changed, run again, compare. Two suites exist.
`scripts/perf-s3-speedtest.sh` times single-object uploads and downloads with
the fil-one s3-speedtests harness. `scripts/perf-drill.sh` runs the
storage-qualification drill: sustained ingest from many workers, with each
block read back shortly after it was written. The numbers are valid for
before/after comparison of your own changes and for finding hotspots. They are
not comparable to staging or production (client and services share one
machine, and there is one piri node), so confirm a finding there before
calling it fixed.

## Prerequisites

- The workspace flow from [DEVELOPING.md](DEVELOPING.md): a `go.work` at the
  `fil-forge/` parent listing `./smelt` plus the services you will change.
- A checkout of the harness each suite drives. The default locations follow
  the Go convention for GitHub clones: with smelt at `<root>/fil-forge/smelt`,
  [fil-one/s3-speedtests](https://github.com/fil-one/s3-speedtests) is
  expected at `<root>/fil-one/s3-speedtests` and
  [fil-one/storage-qualification](https://github.com/fil-one/storage-qualification)
  at `<root>/fil-one/storage-qualification`. Set `S3_SPEEDTESTS_DIR` or
  `STORAGE_QUALIFICATION_DIR` for any other place.
- AWS CLI v2.13+, `jq`, `python3`, and Go for the drill suite (it builds the
  drill from the checkout on every run).
- Disk, per run. Ingot keeps every body blob in its spool (`ingot-data`
  volume, fil-forge/ingot#48) and piri stores a second copy, so a run needs
  about 2.5x the bytes it writes, and nothing is deleted afterwards. For the
  s3-speedtest suite that is 2.5x the file set: a before/after pair on the
  `large` set (25 and 50 GiB objects, roughly 150 GB per run) needs about
  300 GB. For the drill suite it is 2 to 2.5x `STOP_INGEST_AT` (see below). On
  Docker Desktop raise the VM disk limit accordingly. To reclaim the space,
  `make clean` drops every volume (tenant, keys and objects); `make up` and
  `setup` are needed again afterwards.

## The loop

```bash
# Export both for the whole session: every make target reads them, and a
# `make up` without SMELT_WORKSPACE=1 drops the workspace binaries and runs
# the published images instead.
export SMELT_WORKSPACE=1
export SMELT_MANIFEST=manifests/piri-1-postgres-filesystem.yml   # one piri, blobs on disk
make up
./scripts/perf-s3-speedtest.sh setup            # tenant + key + bucket + test files (once per stack)

LABEL=before ./scripts/perf-s3-speedtest.sh run
# edit ingot / sprue / piri ...
make redeploy                                   # or SVC=ingot
LABEL=after ./scripts/perf-s3-speedtest.sh run

./scripts/perf-results.py compare s3-speedtest before after
```

`FILE_SET=quick|standard|large|full` picks the payloads (default `quick`:
one 1 MiB and one 100 MiB file). `RUNS=n` repeats each file. `SNAPSHOT=name`
restores a snapshot before the run so every run starts from the same state;
it re-runs `setup` because hilt's dev vault is in memory and loses the access
key on restore. `make s3-key` then moves to the next free tenant (`perf-2`,
...) and `setup` creates that tenant's bucket (`perf-s3-speedtest-perf-2`),
since ingot bucket names are global and the old one belongs to `perf`. An exported `SMELT_MANIFEST` takes precedence over the
snapshot's own manifest, so the script refuses to restore when the two differ.

## What a run records

`generated/perf-runs/s3-speedtest/<utc-ts>-<label>/`:

- `metadata.json`: git SHA and dirty flag of smelt, ingot, sprue, piri, hilt
  and libforge; which containers run workspace binaries; manifest and piri blob
  backend; Docker server arch, CPUs and memory; aws-cli version and the
  profile's multipart settings; file set and S3 prefix.
- `images.json`: image digests. `docker-df-before.txt`, `docker-df-after.txt`:
  `docker system df` taken before and after the run.
- `stats.csv`: `docker stats` samples (CPU, memory, network, block I/O) for
  ingot, upload, piri-0 and their databases, every 2 seconds.
- `speedtest/`: the s3-speedtests JSONL and logs, `upload.out`,
  `download.out`.
- `logs/<service>.log`: `docker compose logs` for the run window.

Every run also appends one row per (operation, file size) to
`generated/perf-runs/s3-speedtest/runs.jsonl`; `perf-results.py compare`
reads that file. Each run uploads under its own prefix
(`perf/<utc-ts>-<label>/`), so the download phase measures only that run's
objects and nothing is deduplicated across runs.

## The drill suite

The drill writes blocks from many workers at an offered rate, reads each one
back after a short lag, and scores ingest per measurement window. The suite caps
it with the drill's `--stop-ingest-at`, so a run ends after a fixed number of
bytes instead of the profile's full hour.

```bash
make up                                         # with the exports from the loop above
./scripts/perf-drill.sh setup                   # tenant + key + provider .env (once per stack)

LABEL=before ./scripts/perf-drill.sh run
make redeploy
LABEL=after ./scripts/perf-drill.sh run

./scripts/perf-results.py compare drill before after
```

`setup` mints a key for tenant `drill` (AWS CLI profile `smelt-drill`, the
same way `make s3-key` does) and writes it with ingot's endpoint and region to
`generated/perf-runs/drill/provider/.env`, readable only by you. Neither key
is printed. Re-run `setup` after `make down && make up`: hilt's dev vault is
in memory, so the old key stops working and `setup` moves to tenant `drill-2`.

`run` reads these settings from the environment and passes them to the drill
as given:

| Variable | Default | Meaning |
|---|---|---|
| `PROFILE` | `import` | `import`, `smoke` or `import-blocks` |
| `STOP_INGEST_AT` | `60GB` | stop ingesting after this many bytes; GB values only |
| `RAMP` | `10s` | ramp-up before the first measured window |
| `WINDOW` | `10s` | measurement window length |
| `VERIFY_LAG_MIN`, `VERIFY_LAG_MAX` | `30s`, `60s` | when each written block is read back |
| `RATE_TARGET` | the profile's (3 GB/s for import, 1 GB/s for the others) | offered ingest rate, e.g. `5GB` |
| `WORKERS` | a ramp from 64 to 512 | fixed number of blocks in flight |
| `DURATION` | the profile's (1 hour; 15 minutes for smoke) | upper bound on the run |
| `LABEL` | the profile name | run label |

The drill never offers more than `RATE_TARGET`, so a stack faster than that
measures as `RATE_TARGET`. When the median approaches the offered rate shown
in the comparison header, raise `RATE_TARGET`. The drill's 2 GB/s floor and
3 GB/s target do not stop an import run; the p5 metric below reads as the
highest target this run would have met.

The verification lag defaults to 5 to 15 minutes in the drill. A capped local
run (60 GB takes about five minutes at 0.2 GB/s) would end before most reads
fall due and read back almost nothing. With 30 to 60 seconds, read-back runs
alongside ingest within the first minute, and only the blocks written in the
last minute go unread.

The drill scores 60-second windows; the suite uses 10 seconds. A 60 GB run at
0.2 GB/s then has about 30 windows instead of 5, enough for p5 to mean
something, and each window still holds a dozen or more of the import profile's
~128 MiB blobs. Bytes count in the window where their request completes, so a
window much shorter than a blob upload measures completions rather than
throughput. Short windows also show dips that 60-second windows average out,
which makes p5 at 10 seconds stricter than the drill's own scoring; set
`WINDOW=60s` to match it. Aim for at least 20 windows: at a faster stack or a
lower cap, shorten `WINDOW` or raise `STOP_INGEST_AT`.

Plan for 2 to 2.5x `STOP_INGEST_AT` of free disk, and run `make clean` between
large runs. `run` checks the space free on the host and in Docker's disk (the
`ingot-data` volume) and refuses to start with less than 2.5x, because
ingot's spool never frees body bytes and piri frees its copy only minutes
after the drill sweeps its buckets. On Docker Desktop the VM disk is a file on
the host disk, so both need the space.

The drill's evidence always reads "not qualified" under the cap, so `run`
goes by the exit code. 0 means the run completed with no failures and is
recorded. 1 means the evidence records a failure (integrity, early cutoff,
sweep, seal); the run is recorded so the comparison shows it, and the script
exits non-zero. 2 means a usage error or an interrupt, and nothing is
recorded. The objects an interrupted run wrote stay in the stack until
`make clean`.

A drill run records `generated/perf-runs/drill/<utc-ts>-<label>/`:

- `metadata.json`, `images.json`, `docker-df-*.txt`, `stats.csv` and
  `logs/<service>.log` as for s3-speedtest. `metadata.json` also holds the
  storage-qualification SHA, the settings above, the bucket prefix, the
  tenant, the disk estimate and the drill's exit code.
- `drill.out`: the drill's console output.
- `drill/`: the provider directory the drill ran with. `evidence/drill-*.json`
  (the evidence, with every window), `reports/drill-*.md` (the report) and
  `logs/drill-*.jsonl` (one line per window). `.env` links to the shared
  provider file; each run gets its own journal in `state/`, so an interrupted
  run never blocks the next one.

Each run appends one row to `generated/perf-runs/drill/runs.jsonl` with these
metrics:

- ingest GB/s, median: median of the windows' ingest rates.
- ingest GB/s, p5: the highest rate that at least 95% of windows reached, by
  the rule the drill applies to its target. With fewer than 20 windows this is
  the slowest window. It depends on the window length (shown in the comparison
  header), so compare it only between runs with the same `WINDOW` and cap.
- blob_put/s: blob PUT requests per second, as in the drill's report.
- read-back GB/s: bytes read back per second over all windows.
- bytes sent: every byte the drill PUT (blocks plus aggregates; the import
  profiles write no aggregates).
- integrity failures: reads that returned wrong bytes or found a written
  object missing.

Ingest metrics skip windows at the end of the run that ingested nothing. A
stall in the middle of the run stays in.

## Adding a suite

Write `scripts/perf-<name>.sh` with `setup` and `run` commands, source
`scripts/perf-lib.sh` for the run directory, metadata, health wait, stats
sampler and log dump, and record results with
`perf-results.py record <name> <run-dir>`. `perf-results.py` keeps one
function that builds a suite's rows from its run directory and one that
prints its comparison table; add both to `SUITES` for a new suite.
