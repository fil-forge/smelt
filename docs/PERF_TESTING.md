# Performance Testing Against the Local Stack

Smelt can serve as the target of a performance test loop: run a benchmark
against the local ingot, change ingot, sprue or piri in your checkouts,
redeploy only what changed, run again, compare. The numbers are valid for
before/after comparison of your own changes and for finding hotspots. They are
not comparable to staging or production (client and services share one
machine, and there is one piri node), so confirm a finding there before
calling it fixed.

## Prerequisites

- The workspace flow from [DEVELOPING.md](DEVELOPING.md): a `go.work` at the
  `fil-forge/` parent listing `./smelt` plus the services you will change.
- A checkout of [fil-one/s3-speedtests](https://github.com/fil-one/s3-speedtests).
  The default location follows the Go convention for GitHub clones: with smelt
  at `<root>/fil-forge/smelt`, the harness is expected at
  `<root>/fil-one/s3-speedtests`. Set `S3_SPEEDTESTS_DIR` for any other place.
- AWS CLI v2.13+, `jq`, `python3`.
- Disk, per run. Ingot keeps every body blob in its spool (`ingot-data`
  volume) and piri stores a second copy, so one run needs about 2.5x the size
  of its file set. Each run uploads under its own prefix and nothing is
  deleted afterwards, so a before/after pair on the `large` set (25 and 50 GiB
  objects, roughly 150 GB per run) needs about 300 GB. On Docker Desktop raise
  the VM disk limit accordingly. To reclaim the space, `make clean` drops
  every volume (tenant, keys and objects); `make up` and `setup` are needed
  again afterwards.

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

## Adding a suite

Write `scripts/perf-<name>.sh` with `setup` and `run` commands, source
`scripts/perf-lib.sh` for the run directory, metadata, stats sampler and log
dump, and record results with `perf-results.py record <name> <run-dir>`
(it expects s3-speedtests style summary rows; extend it when a suite needs
another shape).
