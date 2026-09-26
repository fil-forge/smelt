# Performance Testing Against the Local Stack

Smelt can serve as the target of a performance test loop: run a benchmark
against the local ingot, change ingot, sprue or piri in your checkouts,
redeploy only what changed, run again, compare. The numbers are valid for
before/after comparison of your own changes and for finding hotspots. They are
not comparable to staging or production (client and services share one
machine, and there is one piri node), so confirm a finding there before
calling it fixed.

| Suite                                 | Measures                                                                              | Harness                                                                           | Script                         |
| ------------------------------------- | ------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- | ------------------------------ |
| [s3-speedtest](#s3-speedtest)         | single-object upload and download times                                               | [fil-one/s3-speedtests](https://github.com/fil-one/s3-speedtests)                 | `scripts/perf-s3-speedtest.sh` |
| [drill](#storage-qualification-drill) | sustained ingest from many workers, each block read back shortly after it was written | [fil-one/storage-qualification](https://github.com/fil-one/storage-qualification) | `scripts/perf-drill.sh`        |

## Prerequisites

- The workspace flow from [DEVELOPING.md](DEVELOPING.md): a `go.work` at the
  `fil-forge/` parent listing `./smelt` plus the services you will change.
- A checkout of the harness each suite drives. The default locations follow
  the Go convention for GitHub clones: with smelt at `<root>/fil-forge/smelt`,
  the harnesses are expected at `<root>/fil-one/s3-speedtests` and
  `<root>/fil-one/storage-qualification`. Set `S3_SPEEDTESTS_DIR` or
  `STORAGE_QUALIFICATION_DIR` for any other place.
- AWS CLI v2.13+, `jq`, `python3`
- Free disk of about 2.5x the bytes a run writes. Ingot keeps every body blob in
  its spool (`ingot-data` volume,
  [fil-forge/ingot#48](https://github.com/fil-forge/ingot/issues/48)) and piri
  stores a second copy, and nothing is deleted after a run. When piri keeps its
  blobs in S3 outside the stack, only the spool grows and about 1.25x is
  enough. On Docker Desktop the VM disk is a file on the host disk, so raise the
  VM disk limit and keep the host disk free too. `make clean` reclaims the space by dropping every
  volume (tenant, keys and objects); run `make up` and the suite's `setup` again
  afterwards.

`make up` and `make redeploy` return only after the services they started
report healthy, so a run can start right after either one.

## What a run records

Each run gets a directory `generated/perf-runs/<suite>/<utc-ts>-<label>/`
with:

- `metadata.json`:
  - full git SHA and dirty flag of smelt, ingot, sprue, piri, hilt and
    libforge (null for a checkout that is not there)
  - which containers run workspace binaries
  - manifest and piri blob backend
  - Docker server arch, CPUs, memory, version, storage driver and root
    directory under `docker`; kernel (`uname -r`) and the OS Docker reports
    under `system`
  - `piri.s3_endpoint` and `piri.indexer` from piri-0's environment (`on`
    unless `PIRI_INDEXER` says otherwise), and `sprue.indexer_endpoint` from
    upload's; an empty string means the variable is set empty, null that it
    is unset. Credentials are never read.
  - `images`: the contents of `images.lock.json`
  - `extra`: `PERF_EXTRA_METADATA`, verbatim
  - the suite's settings.
- `images.lock.json`: one entry per compose container, exited one-shots
  included: `service`, `ref` (the image as compose named it), `digest`,
  `revision` and `source` (the `org.opencontainers.image.revision` and
  `.source` labels, null when the image has none), `created`, `arch` and
  `repo_digests`. `digest` is the ref's own when the ref pins one, and the
  first repo digest otherwise.
- `images.json`: `docker compose images` output.
- `docker-df-before.txt`, `docker-df-after.txt`: `docker system df` taken
  before and after the run.
- `stats.csv`: `docker stats` samples (CPU, memory, network, block I/O) for
  ingot, upload, piri-0 and their databases, every 2 seconds.
- `logs/<service>.log`: `docker compose logs` for the run window.

Every run also appends its results to `generated/perf-runs/<suite>/runs.jsonl`,
which `perf-results.py compare` reads. Each row carries `images` (ref, digest
and revision per service) and `extra`. The comparison header shows the first
nine characters of each SHA; for a run on published images it shows each
image's revision in place of the sibling checkouts. The suite sections below
list what each suite adds.

`PERF_EXTRA_METADATA` attaches a caller's own facts to a run, such as a run ID
or the machine it ran on. It must be a single JSON object; anything else stops
the run before it creates a run directory. smelt attaches no meaning to it.

## s3-speedtest

Times single-object uploads and downloads with the s3-speedtests harness.

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

### setup

`setup` mints a key for tenant `perf` (AWS CLI profile `smelt-perf`, the same
way `make s3-key` does), creates the bucket and generates the test files in
`generated/perf/testfiles`. Re-run it after `make down && make up`: hilt's dev
vault is in memory, so the old key stops working. `setup` then moves to the
next free tenant (`perf-2`, ...) and creates that tenant's bucket
(`perf-s3-speedtest-perf-2`), since ingot bucket names are global and the old
one belongs to `perf`.

### run

`run` reads these settings from the environment:

| Variable        | Default                    | Meaning                                                                                                                          |
| --------------- | -------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `LABEL`         | (required)                 | run label                                                                                                                        |
| `FILE_SET`      | `quick`                    | `quick` (1 MiB + 100 MiB), `standard` (~1.6 GiB of 1 MiB to 1 GiB files), `large` (25 GiB + 50 GiB) or `full` (standard + large) |
| `RUNS`          | `1`                        | repeats per file                                                                                                                 |
| `SNAPSHOT`      | (none)                     | snapshot to restore before the run, so every run starts from the same state                                                      |
| `TESTFILES_DIR` | `generated/perf/testfiles` | where the payloads live                                                                                                          |

The `large` set writes 75 GiB per run, so plan for about 200 GB of free disk
per run and 400 GB for a before/after pair.

`SNAPSHOT` runs `make down && make up SNAPSHOT=...` and then `setup` again,
because the restore loses hilt's access key the same way a restart does. An
exported `SMELT_MANIFEST` takes precedence over the snapshot's own manifest,
so the script refuses to restore when the two differ.

Besides the [common files](#what-a-run-records), a run records `speedtest/`
(the s3-speedtests JSONL and logs, `upload.out`, `download.out`) and appends
one row per (operation, file size) to `runs.jsonl`. Each run uploads under its
own prefix (`perf/<utc-ts>-<label>/`), so the download phase measures only
that run's objects and nothing is deduplicated across runs. `metadata.json`
also holds the file set, the prefix and bucket, the aws-cli version, the
profile's multipart settings and the s3-speedtests SHA.

## Storage-qualification drill

The drill writes blobs from many workers at an offered rate, reads each one
back after a short lag, and scores ingest per measurement window. The suite
caps it with the drill's `--stop-ingest-at`, so a run stops ingesting after
50 GB by default instead of running the profile's full hour.

```bash
# Export both for the whole session: every make target reads them, and a
# `make up` without SMELT_WORKSPACE=1 drops the workspace binaries and runs
# the published images instead.
export SMELT_WORKSPACE=1
export SMELT_MANIFEST=manifests/piri-1-postgres-filesystem.yml   # one piri, blobs on disk
make up
./scripts/perf-drill.sh setup                   # tenant + key + provider .env (once per stack)

LABEL=before ./scripts/perf-drill.sh run
# edit ingot / sprue / piri ...
make clean                                      # delete object data, tenants and keys
make redeploy                                   # or SVC=ingot
./scripts/perf-drill.sh setup                   # new key: make clean dropped the old one
LABEL=after ./scripts/perf-drill.sh run

./scripts/perf-results.py compare drill before after
```

### setup

`setup` mints a key for tenant `drill` (AWS CLI profile `smelt-drill`, the
same way `make s3-key` does) and writes it with ingot's endpoint and region to
`generated/perf-runs/drill/provider/.env`, readable only by you. Neither key
is printed. Re-run `setup` after `make down && make up`: hilt's dev vault is
in memory, so the old key stops working and `setup` moves to tenant `drill-2`.
Re-run it after `make clean` too, which deletes the tenant along with its key;
`setup` then creates tenant `drill` again. Without it, `run` stops at its
smoke check with `InvalidAccessKeyId`.

### run

`run` reads these settings from the environment and passes them to the drill
as given:

| Variable                           | Default          | Meaning                                                                                              |
| ---------------------------------- | ---------------- | ---------------------------------------------------------------------------------------------------- |
| `LABEL`                            | the profile name | run label                                                                                            |
| `PROFILE`                          | `import`         | `import`, `smoke` or `import-blocks`                                                                 |
| `STOP_INGEST_AT`                   | `50GB`           | stop ingesting after this many bytes; GB values only                                                 |
| `RAMP`                             | `10s`            | ramp-up before the first measured window                                                             |
| `WINDOW`                           | `10s`            | measurement window length                                                                            |
| `VERIFY_LAG_MIN`, `VERIFY_LAG_MAX` | `30s`, `60s`     | when each written block is read back                                                                 |
| `RATE_TARGET`                      | the profile's    | offered ingest rate, e.g. `5GB`                                                                      |
| `WORKERS`                          | `16`             | fixed number of blobs in flight (the drill ramps from 64 to 512, more than a laptop stack can serve) |
| `DURATION`                         | `15m`            | upper bound on the run, so a stack too slow to reach the cap still finishes in minutes               |
| `KEEP_OBJECTS`                     | unset            | `1` passes `--keep-objects`: the drill skips its sweep, and the objects stay until `make clean`      |
| `ENFORCE_FLOOR`                    | the profile's    | `true` or `false`: fail the run when a window falls below the floor                                  |
| `PROGRESS`                         | once per window  | how often the drill prints a progress line, e.g. `30s`; `0` prints none                              |
| `ACCOUNTS`, `RESTORE_SCALE`        | the profile's    | restore accounts, and the scale of the restore cohort sizes                                          |
| `SIZE_MEAN`, `SIZE_SIGMA`, `SIZE_MIN`, `SIZE_MAX` | the profile's | block size distribution; the `import` profile refuses them, since its packing model sets the blob sizes |
| `AGGREGATE_SIZE`, `AGGREGATE_EVERY` | the profile's   | aggregate size and blocks per aggregate; `import` has no aggregates                                  |
| `CONFIG_NOTE`                      | unset            | one line the drill records in its evidence                                                           |
| `DISK_FACTOR`                      | `2.5` or `1.25`  | free disk the run needs, as a multiple of `STOP_INGEST_AT`; see below                                |

Every variable in the table reaches the drill only when set, so with none of
the new ones set the command line is the one above. Before it starts, `run`
checks that ingot's `/data` volume has `DISK_FACTOR` times `STOP_INGEST_AT`
free, and under Docker Desktop the host filesystem too; elsewhere Docker's
volumes are not under the checkout, so the host is not measured.
`DISK_FACTOR` defaults to 1.25 when piri-0's blob backend is `s3` with an
endpoint other than the stack's `piri-minio`, and to 2.5 otherwise.

On a dedicated Linux host, set `INGOT_URL` to ingot's container address (for
example `http://172.18.0.5:80`) before `setup`, so the drill reaches ingot
directly instead of through Docker's userland proxy. Extra compose files chain
through `COMPOSE_FILE` (`COMPOSE_FILE=compose.yml:extra.yml`) while
`SMELT_WORKSPACE` is off; with it on, the Makefile names the files itself.

While it runs, the drill prints a progress line per window: the phase (ramp,
steady, read-back after the cap), bytes written and read back, the last
window's rates and the number of failed requests. At the end `run` prints the
run's comparison table and the path of the drill's report,
`drill/reports/drill-*.md`, the same report a full-size run produces.

The cap marks every run's evidence "not qualified", so `run` takes the
verdict from the drill's exit code, which the storage-qualification
[MANUAL](https://github.com/fil-one/storage-qualification/blob/main/MANUAL.md)
defines. `run` exits non-zero for anything but 0.

| Drill exit code | Meaning                       | Recorded                                                                               |
| --------------- | ----------------------------- | -------------------------------------------------------------------------------------- |
| 0               | completed with no failures    | yes                                                                                    |
| 1               | a failure                     | yes, so the comparison shows it; nothing when the drill failed before writing evidence |
| 2               | a usage error or an interrupt | no; objects an interrupted run wrote stay in the stack until `make clean`              |

Besides the [common files](#what-a-run-records), a run records:

- `drill.out`: the drill's console output.
- `drill/`: the provider directory the drill ran with. `evidence/drill-*.json`
  (the evidence, with every window), `reports/drill-*.md` (the report) and
  `logs/drill-*.jsonl` (one line per window). `.env` links to the shared
  provider file; each run gets its own journal in `state/`, so an interrupted
  run never blocks the next one.

`metadata.json` also holds the storage-qualification SHA, the settings above
(null when unset), the drill's full command line as `argv`, the tenant, the
disk check (`disk`: the factor applied, piri-0's blob backend and S3 endpoint,
and the free space measured, with `host_free_gb` null when the host was not
measured) and the drill's exit code. Each run appends one
row to `runs.jsonl`, with the drill's settings under `settings`, the Docker
and system facts under `host`, and `manifest`, `piri` and `sprue` as in
`metadata.json`. Rows written before these fields existed lack them, and
`compare` reads both kinds.

### Reading drill results

The rates in `runs.jsonl` are the ones in the drill report's Sustained rates
section, which the drill also records in its evidence. The
storage-qualification
[DESIGN](https://github.com/fil-one/storage-qualification/blob/main/DESIGN.md)
says which windows they cover and how p5 is scored.

**The ingest rate never exceeds `RATE_TARGET`.** The drill offers no more
than that, so a faster stack measures as `RATE_TARGET`. When the median
approaches the offered rate shown in the comparison header, raise
`RATE_TARGET`.

**Aim for at least 20 steady windows.** p5 over fewer than 20 windows is
simply the slowest one. A 50 GB run at 0.2 GB/s ingests for about four
minutes, which gives about 25 windows of 10 seconds, each still holding a
dozen or more of the import profile's ~128 MiB blobs. On a faster stack or
with a lower cap, shorten `WINDOW` or raise `STOP_INGEST_AT`. Bytes count in
the window where their request completes, so a window much shorter than a
blob upload measures completions rather than throughput.

**p5 depends on the window length.** Staging runs use 60-second windows.
Rates (ingest, read-back and restore GB/s, writes a second) read the same at
either length, but 10-second windows show dips that 60-second windows average
out, so p5 from a laptop run is stricter than p5 from a staging run of the
same stack. Set `WINDOW=60s` to score the way staging does, and compare p5
only between runs with the same `WINDOW` and `STOP_INGEST_AT`; the comparison
header shows both.

**Read-back overlaps ingest because of the short verification lag.** After
the cap the drill keeps running until every blob it wrote has been read back.
With the drill's default lag of 5 to 15 minutes, a few minutes of ingest
would see no reads at all, followed by up to a quarter of an hour of reads
alone. With 30 to 60 seconds, read-back runs alongside ingest within the
first minute, as it does in a long run, and the run ends about a minute after
the cap. Reads still start one lag after the first writes, so with only a
handful of steady windows the read-back median understates the read rate.

Bytes sent counts every byte the drill sent, including writes the store
failed. The request and error counts cover the whole run.

## Adding a suite

Write `scripts/perf-<name>.sh` with `setup` and `run` commands, source
`scripts/perf-lib.sh` for the run directory, metadata, stats sampler and log
dump, and record results with `perf-results.py record <name> <run-dir>`.
`perf-results.py` keeps one function that builds a suite's rows from its run
directory and one that prints its comparison table; add both to `SUITES` for
a new suite.
