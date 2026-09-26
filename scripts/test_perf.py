"""Tests for the perf suites' recording: perf-lib.sh against a stub docker, and
perf-results.py over a mix of rows written before and after images were
recorded. Run with `python3 -m unittest test_perf` from scripts/ (go test
./scripts runs it too).
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.dont_write_bytecode = True
SCRIPTS = Path(__file__).resolve().parent
TESTDATA = SCRIPTS / "testdata" / "perf"

# A stub docker for perf-lib.sh. Containers and images come from the JSON
# fixtures; one container's image is gone, so `image inspect`
# prints the rest and exits 1 as docker does. STUB_<VAR> sets a variable
# inside the containers (set to empty means set empty; unset means unset).
STUB_DOCKER = r"""#!/usr/bin/env bash
case "$1 $2" in
  "version --format") case "$3" in *Arch*) echo arm64 ;; *) echo 28.0.0 ;; esac ;;
  "info --format")
    case "$3" in
      *NCPU*) echo 8 ;; *MemTotal*) echo 33000000000 ;; *Driver*) echo overlay2 ;;
      *DockerRootDir*) echo /var/lib/docker ;; *OperatingSystem*) echo "Ubuntu 24.04.3 LTS" ;;
    esac ;;
  "compose images") echo '[{"ContainerName":"smelt-ingot-1"}]' ;;
  "compose ps") printf 'c-ingot\nc-piri\nc-init\nc-gone\n' ;;
  "inspect "*) cat "$STUB_FIXTURES/containers.json" ;;
  "image inspect") cat "$STUB_FIXTURES/images.json"; exit 1 ;;
  "compose exec")
    var="STUB_${!#}"
    [ -n "${!var+x}" ] || exit 1
    echo "${!var}" ;;
  "system df") echo "TYPE TOTAL" ;;
  *) echo "stub docker: unexpected $*" >&2; exit 64 ;;
esac
"""


class PerfLibTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        # A project of its own, so the real checkout's generated/ is never read.
        self.project = self.tmp / "smelt"
        (self.project / "scripts").mkdir(parents=True)
        for name in ("perf-lib.sh", "perf-drill.sh"):
            shutil.copy(SCRIPTS / name, self.project / "scripts" / name)
        (self.project / "smelt.yml").write_text("version: 1\npiri:\n  nodes:\n    - storage: {db: postgres, blob: s3}\n")
        git = ["git", "-C", str(self.project), "-c", "user.name=t", "-c", "user.email=t@example.com"]
        subprocess.run(git + ["init", "-q"], check=True)
        subprocess.run(git + ["add", "-A"], check=True)
        subprocess.run(git + ["commit", "-q", "-m", "fixture"], check=True)
        stub = self.tmp / "bin"
        stub.mkdir()
        (stub / "docker").write_text(STUB_DOCKER)
        # perf-drill.sh requires these before it validates anything; none may run.
        for tool in ("aws", "go"):
            (stub / tool).write_text("#!/bin/sh\necho unexpected >&2\nexit 64\n")
        for f in stub.iterdir():
            f.chmod(0o755)
        self.env = {
            k: v for k, v in os.environ.items()
            if not k.startswith(("STUB_", "PERF_", "SMELT_", "COMPOSE_"))
        }
        self.env["PATH"] = f"{stub}{os.pathsep}{os.environ['PATH']}"
        self.env["STUB_FIXTURES"] = str(TESTDATA)
        self.run_dir = self.tmp / "run"
        self.run_dir.mkdir()

    def bash(self, script: str, **env: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", "-c", f"set -euo pipefail; source scripts/perf-lib.sh; {script}"],
            cwd=self.project, env={**self.env, **env}, capture_output=True, text=True,
        )

    def metadata(self, **env: str) -> dict:
        res = self.bash(f"perf_metadata {self.run_dir} lbl '{{\"profile\":\"import\"}}'", **env)
        self.assertEqual(res.returncode, 0, res.stderr)
        return json.loads((self.run_dir / "metadata.json").read_text())

    def test_metadata_records_images_host_and_services(self):
        meta = self.metadata(STUB_PIRI_S3_ENDPOINT="s3.us-east-2.amazonaws.com", STUB_SPRUE_INDEXER_ENDPOINT="")
        self.assertEqual(len(meta["repos"]["smelt"]["sha"]), 40)
        self.assertIsNone(meta["repos"]["ingot"])
        by_service = {i["service"]: i for i in meta["images"]}
        self.assertEqual(sorted(by_service), ["gone", "ingot", "piri-0", "upload-init"])
        # Tag ref: the digest is the pulled repo digest; the label gives the revision.
        self.assertEqual(by_service["ingot"]["digest"], "sha256:" + "a" * 64)
        self.assertEqual(by_service["ingot"]["revision"], "0123456789abcdef0123456789abcdef01234567")
        self.assertEqual(by_service["ingot"]["source"], "https://github.com/fil-forge/ingot")
        # Digest ref: the ref's own digest wins over the repo digests.
        self.assertEqual(by_service["piri-0"]["digest"], "sha256:" + "c" * 64)
        # An exited one-shot on a third-party image: recorded, no revision label.
        self.assertEqual(by_service["upload-init"]["ref"], "postgres:16-alpine")
        self.assertIsNone(by_service["upload-init"]["revision"])
        self.assertEqual(by_service["upload-init"]["digest"], "sha256:" + "e" * 64)
        # An image docker no longer has keeps what the container said.
        self.assertEqual(by_service["gone"]["ref"], "redis:7-alpine")
        self.assertIsNone(by_service["gone"]["digest"])
        self.assertEqual(json.loads((self.run_dir / "images.lock.json").read_text()), meta["images"])
        self.assertTrue((self.run_dir / "images.json").exists())
        self.assertEqual(meta["docker"]["storage_driver"], "overlay2")
        self.assertEqual(meta["docker"]["root_dir"], "/var/lib/docker")
        self.assertEqual(meta["system"]["os"], "Ubuntu 24.04.3 LTS")
        self.assertTrue(meta["system"]["kernel"])
        self.assertEqual(meta["piri"], {"s3_endpoint": "s3.us-east-2.amazonaws.com", "indexer": "on"})
        self.assertEqual(meta["sprue"], {"indexer_endpoint": ""})
        self.assertIsNone(meta["extra"])

    def test_metadata_keeps_existing_keys(self):
        meta = self.metadata()
        self.assertEqual(meta["label"], "lbl")
        self.assertEqual(meta["manifest"], "smelt.yml")
        self.assertEqual(meta["piri_blob_backend"], "s3")
        self.assertEqual(meta["docker"]["ncpu"], "8")
        self.assertEqual(meta["suite"], {"profile": "import"})
        self.assertEqual(meta["workspace_services"], [])
        self.assertIsInstance(meta["host"], str)
        self.assertEqual(meta["piri"], {"s3_endpoint": None, "indexer": "on"})
        self.assertEqual(meta["sprue"], {"indexer_endpoint": None})

    def test_extra_lands_verbatim(self):
        extra = {"run_id": "main-20261001t120312z", "client_path": "container-ip", "nested": {"n": 1}}
        meta = self.metadata(PERF_EXTRA_METADATA=json.dumps(extra), STUB_PIRI_INDEXER="off")
        self.assertEqual(meta["extra"], extra)
        self.assertEqual(meta["piri"]["indexer"], "off")

    def test_check_extra_rejects_anything_but_one_object(self):
        for value in ("[1]", "5", '"s"', "null", "not json", '{"a":1} {"b":2}', "5 {}", " "):
            with self.subTest(value=value):
                res = self.bash("perf_check_extra", PERF_EXTRA_METADATA=value)
                self.assertNotEqual(res.returncode, 0)
                self.assertIn("PERF_EXTRA_METADATA must be a single JSON object", res.stderr)
        for value in ("", "{}", '{"a": [1, 2]}'):
            with self.subTest(value=value):
                self.assertEqual(self.bash("perf_check_extra", PERF_EXTRA_METADATA=value).returncode, 0)

    def test_drill_run_stops_before_a_run_directory_on_bad_extra(self):
        res = subprocess.run(
            ["bash", "scripts/perf-drill.sh", "run"], cwd=self.project,
            env={**self.env, "PERF_EXTRA_METADATA": "[1]"}, capture_output=True, text=True,
        )
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("PERF_EXTRA_METADATA must be a single JSON object", res.stderr)
        self.assertNotIn("unexpected", res.stderr)
        self.assertFalse((self.project / "generated" / "perf-runs").exists())


# Stubs for a whole `perf-drill.sh run`. docker answers what the drill suite
# asks and hands everything else to STUB_DOCKER; df reports STUB_HOST_FREE_KB;
# aws keeps one object in STUB_S3; go "builds" the drill by copying the stub
# drill, which writes its argv, NUL-separated, to STUB_DRILL_ARGV and exits
# STUB_DRILL_EXIT, after writing the evidence fixture when that is 0.
DRILL_STUBS = {
    "docker": r"""#!/usr/bin/env bash
case "$*" in
  "info --format {{.OperatingSystem}}") echo "${STUB_OS:-Ubuntu 24.04.3 LTS}" ;;
  "compose exec -T ingot df -Pk /data")
    printf 'Filesystem 1024-blocks Used Available Capacity Mounted\nov 1 1 %s 1%% /data\n' "$STUB_DOCKER_FREE_KB" ;;
  "compose ps -q "*) echo "c-$4" ;;
  "stats "*) echo "c,1%,1MiB,0B,0B" ;;
  "compose logs "*) echo log ;;
  *) exec "$STUB_BASE_DOCKER" "$@" ;;
esac
""",
    "df": r"""#!/bin/sh
printf 'Filesystem 1024-blocks Used Available Capacity Mounted\nhost 1 1 %s 1%% /\n' "$STUB_HOST_FREE_KB"
""",
    "aws": r"""#!/usr/bin/env bash
if [ "$1 $2" = "configure get" ]; then
  case "${!#}" in endpoint_url) echo http://localhost:15130 ;; region) echo us-east-1 ;; *) echo t ;; esac
  exit 0
fi
[ "$1" = --profile ] && shift 2
src="${*: -2:1}" dst="${*: -1}"
case "$1 $2" in
  "s3 cp") case "$src" in s3://*) cp "$STUB_S3" "$dst" ;; *) cp "$src" "$STUB_S3" ;; esac ;;
esac
""",
    "go": r"""#!/bin/sh
cp "$STUB_DRILL" bin/drill
""",
}
STUB_DRILL = r"""#!/usr/bin/env bash
[ "${1:-}" = --help ] && { echo "  -stop-ingest-at string"; exit 0; }
printf '%s\0' "$0" "$@" > "$STUB_DRILL_ARGV"
if [ "${STUB_DRILL_EXIT:-2}" = 0 ]; then
  mkdir -p "$2/evidence" && cp "$STUB_EVIDENCE" "$2/evidence/drill-x.json"
fi
exit "${STUB_DRILL_EXIT:-2}"
"""

# The drill command line before any drill flag variable existed.
DEFAULT_TAIL = [
    "--profile", "import", "--stop-ingest-at", "50GB", "--ramp", "10s", "--window", "10s",
    "--verify-lag-min", "30s", "--verify-lag-max", "60s", "--workers", "16", "--duration", "15m",
]


class PerfDrillRunTest(unittest.TestCase):
    def setUp(self):
        # The same project, git repo and stub directory as PerfLibTest.
        PerfLibTest.setUp(self)
        shutil.copy(SCRIPTS / "perf-results.py", self.project / "scripts" / "perf-results.py")
        stub = self.tmp / "bin"
        shutil.move(stub / "docker", self.tmp / "base-docker")
        for name, text in DRILL_STUBS.items():
            (stub / name).write_text(text)
            (stub / name).chmod(0o755)
        (self.tmp / "drill").write_text(STUB_DRILL)
        (self.tmp / "drill").chmod(0o755)
        sq = self.tmp / "sq"
        (sq / "cmd" / "drill").mkdir(parents=True)
        (sq / "bin").mkdir()
        provider = self.project / "generated" / "perf-runs" / "drill" / "provider"
        provider.mkdir(parents=True)
        (provider / ".env").write_text("SQ_ENDPOINT=localhost:15130\n")
        self.argv_file = self.tmp / "argv"
        self.env.update(
            STORAGE_QUALIFICATION_DIR=str(sq), STUB_BASE_DOCKER=str(self.tmp / "base-docker"),
            STUB_DRILL=str(self.tmp / "drill"), STUB_DRILL_ARGV=str(self.argv_file),
            STUB_EVIDENCE=str(TESTDATA / "drill-evidence.json"), STUB_S3=str(self.tmp / "object"),
            STUB_HOST_FREE_KB="200000000", STUB_DOCKER_FREE_KB="300000000",
        )
        for var in ("PROFILE", "STOP_INGEST_AT", "RAMP", "WINDOW", "VERIFY_LAG_MIN", "VERIFY_LAG_MAX",
                    "RATE_TARGET", "WORKERS", "DURATION", "LABEL", "KEEP_OBJECTS", "ENFORCE_FLOOR",
                    "PROGRESS", "ACCOUNTS", "RESTORE_SCALE", "SIZE_MEAN", "SIZE_SIGMA", "SIZE_MIN",
                    "SIZE_MAX", "AGGREGATE_SIZE", "AGGREGATE_EVERY", "CONFIG_NOTE", "DISK_FACTOR"):
            self.env.pop(var, None)

    def run_drill(self, **env: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", "scripts/perf-drill.sh", "run"], cwd=self.project,
            env={**self.env, **env}, capture_output=True, text=True, timeout=60,
        )

    def run_and_read(self, **env: str) -> tuple[subprocess.CompletedProcess, dict, list[str]]:
        res = self.run_drill(**env)
        runs = sorted((self.project / "generated" / "perf-runs" / "drill").glob("*-*/metadata.json"))
        self.assertEqual(len(runs), 1, res.stderr)
        argv = self.argv_file.read_text().split("\0")[:-1]
        return res, json.loads(runs[0].read_text())["suite"], argv

    def test_unset_flags_keep_the_command_line_and_disk_numbers(self):
        res, suite, argv = self.run_and_read(STUB_OS="Docker Desktop")
        self.assertEqual(res.returncode, 1)
        self.assertIn("usage error or an interrupt (exit 2)", res.stderr)
        self.assertEqual(argv[1:3], ["--provider", str(Path(argv[2]))])
        self.assertTrue(argv[2].endswith("/drill"))
        self.assertEqual(argv[3:], DEFAULT_TAIL)
        self.assertEqual(suite["argv"], argv)
        self.assertEqual(suite["drill_exit"], 2)
        self.assertEqual(suite["disk"], {
            "needed_gb": 125.0, "host_free_gb": 204.8, "docker_free_gb": 307.2,
            "factor": 2.5, "backend": "filesystem", "endpoint": None,
        })
        self.assertIn("disk: need ~125.0 GB; host has 204.8 GB free, Docker 307.2 GB", res.stdout)
        for key in ("keep_objects", "enforce_floor", "progress", "accounts", "restore_scale", "size_mean",
                    "size_sigma", "size_min", "size_max", "aggregate_size", "aggregate_every",
                    "config_note", "disk_factor", "rate_target"):
            self.assertIsNone(suite[key], key)

    def test_linux_host_is_not_measured(self):
        # A host too small to pass the old check does not matter off Docker Desktop.
        res, suite, _ = self.run_and_read(STUB_HOST_FREE_KB="1000")
        self.assertIsNone(suite["disk"]["host_free_gb"])
        self.assertIn("disk: need ~125.0 GB; Docker has 307.2 GB free", res.stdout)
        res = self.run_drill(STUB_OS="Docker Desktop", STUB_HOST_FREE_KB="1000000")
        self.assertIn("the host has 1.0 GB and Docker's disk has 307.2 GB", res.stderr)

    def test_disk_factor_follows_where_piri_keeps_blobs(self):
        cases = [
            ({"STUB_PIRI_BLOB_BACKEND": "s3", "STUB_PIRI_S3_ENDPOINT": "s3.us-east-2.amazonaws.com"}, 1.25, None),
            ({"STUB_PIRI_BLOB_BACKEND": "s3", "STUB_PIRI_S3_ENDPOINT": "piri-minio:9000"}, 2.5, None),
            ({"STUB_PIRI_BLOB_BACKEND": "s3", "STUB_PIRI_S3_ENDPOINT": "s3.x", "DISK_FACTOR": "1.5"}, 1.5, 1.5),
        ]
        for env, factor, recorded in cases:
            with self.subTest(env=env):
                for old in (self.project / "generated" / "perf-runs" / "drill").glob("*-*"):
                    if old.name != "provider":
                        shutil.rmtree(old)
                _, suite, _ = self.run_and_read(STOP_INGEST_AT="100GB", **env)
                self.assertEqual(suite["disk"]["factor"], factor)
                self.assertEqual(suite["disk"]["needed_gb"], 100 * factor)
                self.assertEqual(suite["disk"]["backend"], "s3")
                self.assertEqual(suite["disk"]["endpoint"], env["STUB_PIRI_S3_ENDPOINT"])
                self.assertEqual(suite["disk_factor"], recorded)
        res = self.run_drill(STOP_INGEST_AT="300GB", STUB_PIRI_BLOB_BACKEND="s3", STUB_PIRI_S3_ENDPOINT="s3.x")
        self.assertIn("needs about 375.0 GB free (DISK_FACTOR=1.25), but Docker's disk has 307.2 GB", res.stderr)

    def test_every_flag_reaches_the_drill_and_the_record(self):
        env = {
            "RATE_TARGET": "5GB", "KEEP_OBJECTS": "1", "ENFORCE_FLOOR": "false", "PROGRESS": "30s",
            "ACCOUNTS": "64", "RESTORE_SCALE": "0.25", "SIZE_MEAN": "1MB", "SIZE_SIGMA": "1.0",
            "SIZE_MIN": "4KiB", "SIZE_MAX": "16MiB", "AGGREGATE_SIZE": "8MB", "AGGREGATE_EVERY": "100",
            "CONFIG_NOTE": "main-20261001t120312z, box tier1",
        }
        _, suite, argv = self.run_and_read(**env)
        self.assertEqual(argv[3:], DEFAULT_TAIL + [
            "--rate-target", "5GB", "--keep-objects", "--enforce-floor=false", "--progress", "30s",
            "--accounts", "64", "--restore-scale", "0.25", "--size-mean", "1MB", "--size-sigma", "1.0",
            "--size-min", "4KiB", "--size-max", "16MiB", "--aggregate-size", "8MB",
            "--aggregate-every", "100", "--config-note", "main-20261001t120312z, box tier1",
        ])
        self.assertEqual(suite["argv"], argv)
        self.assertEqual(
            {k: suite[k] for k in ("keep_objects", "enforce_floor", "progress", "accounts", "restore_scale",
                                   "size_mean", "size_sigma", "size_min", "size_max", "aggregate_size",
                                   "aggregate_every", "config_note", "rate_target")},
            {"keep_objects": True, "enforce_floor": False, "progress": "30s", "accounts": 64,
             "restore_scale": 0.25, "size_mean": "1MB", "size_sigma": 1.0, "size_min": "4KiB",
             "size_max": "16MiB", "aggregate_size": "8MB", "aggregate_every": 100,
             "config_note": "main-20261001t120312z, box tier1", "rate_target": "5GB"})

    def test_keep_objects_run_records_and_says_what_stays(self):
        res, suite, argv = self.run_and_read(KEEP_OBJECTS="1", STUB_DRILL_EXIT="0")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertIn("--keep-objects", argv)
        self.assertIn("stay in the stack until 'make clean'", res.stdout)
        row = json.loads((self.project / "generated" / "perf-runs" / "drill" / "runs.jsonl").read_text())
        self.assertTrue(row["settings"]["keep_objects"])
        self.assertIsNone(row["settings"]["size_mean"])

    def test_size_flags_reach_the_drill_for_import(self):
        # The wrapper leaves the refusal to the drill, which names the profile.
        _, suite, argv = self.run_and_read(PROFILE="import", SIZE_MEAN="1MB")
        self.assertEqual(argv[-2:], ["--size-mean", "1MB"])
        self.assertEqual(suite["size_mean"], "1MB")

    def test_bad_values_stop_before_a_run_directory(self):
        cases = {
            "KEEP_OBJECTS": "yes", "ENFORCE_FLOOR": "1", "PROGRESS": "30", "ACCOUNTS": "0",
            "AGGREGATE_EVERY": "-1", "RESTORE_SCALE": "0", "DISK_FACTOR": "x", "SIZE_SIGMA": "-1",
            "SIZE_MEAN": "big", "AGGREGATE_SIZE": "1XB", "CONFIG_NOTE": "two\nlines",
        }
        for var, value in cases.items():
            with self.subTest(var=var):
                res = self.run_drill(**{var: value})
                self.assertNotEqual(res.returncode, 0)
                self.assertIn(var, res.stderr)
                self.assertFalse(self.argv_file.exists())
                self.assertEqual(
                    [p.name for p in (self.project / "generated" / "perf-runs" / "drill").iterdir()],
                    ["provider"])


def load_perf_results():
    spec = importlib.util.spec_from_file_location("perf_results", SCRIPTS / "perf-results.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PerfResultsTest(unittest.TestCase):
    def setUp(self):
        self.pr = load_perf_results()
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.pr.RUNS_ROOT = self.tmp / "perf-runs"
        (self.pr.RUNS_ROOT / "drill").mkdir(parents=True)
        # A row as runs.jsonl held it before images were recorded.
        old = json.loads((TESTDATA / "old-drill-row.json").read_text())
        (self.pr.RUNS_ROOT / "drill" / "runs.jsonl").write_text(json.dumps(old) + "\n")

    def new_run(self, workspace_services: list[str]) -> Path:
        run_dir = self.tmp / "run"
        evidence = run_dir / "drill" / "evidence"
        evidence.mkdir(parents=True)
        shutil.copy(TESTDATA / "drill-evidence.json", evidence / "drill-x.json")
        meta = json.loads((TESTDATA / "new-metadata.json").read_text())
        meta["workspace_services"] = workspace_services
        (run_dir / "metadata.json").write_text(json.dumps(meta))
        return run_dir

    def compare(self) -> str:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(self.pr.main(["perf-results.py", "compare", "drill"]), 0)
        return out.getvalue()

    def test_new_row_carries_settings_host_images_and_extra(self):
        run_dir = self.new_run([])
        with contextlib.redirect_stdout(io.StringIO()):
            row = self.pr.record("drill", run_dir, self.pr.drill_rows)[0]
        self.assertEqual(row["settings"]["ramp"], "10s")
        self.assertEqual(row["settings"]["workers"], 16)
        self.assertIsNone(row["settings"]["keep_objects"])
        self.assertEqual(row["host"]["name"], "box")
        self.assertEqual(row["host"]["storage_driver"], "overlay2")
        self.assertEqual(row["host"]["kernel"], "6.8.0-1024-aws")
        self.assertEqual(row["manifest"], "/var/lib/forge-perf/run/smelt.yml")
        self.assertEqual(row["piri"]["s3_endpoint"], "s3.us-east-2.amazonaws.com")
        self.assertEqual(row["sprue"], {"indexer_endpoint": ""})
        self.assertEqual(row["images"]["ingot"]["revision"], "0123456789abcdef0123456789abcdef01234567")
        self.assertEqual(row["extra"], {"run_id": "main-20261001t120312z"})
        # Every key an old row had is still there.
        old = json.loads((TESTDATA / "old-drill-row.json").read_text())
        self.assertLessEqual(set(old) - {"run_dir"}, set(row))

    def test_compare_over_old_and_new_rows_published_images(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.pr.record("drill", self.new_run([]), self.pr.drill_rows)
        out = self.compare()
        # The old row keeps its sibling checkouts; the new one shows image revisions.
        self.assertIn("[ingot@1a2b3c4, smelt@5d6e7f8*]", out)
        self.assertIn("[smelt@fedcba987, ingot@012345678, upload@89abcdef0]", out)
        self.assertIn("storage-qualification@5cfeaf3aa", out)
        self.assertIn("before", out)
        self.assertIn("after", out)

    def test_compare_workspace_run_keeps_checkouts(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.pr.record("drill", self.new_run(["ingot"]), self.pr.drill_rows)
        out = self.compare()
        self.assertIn("[ingot@aaaaaaaaa*, smelt@fedcba987]  workspace: ingot", out)

    def test_speedtest_rows_carry_images_and_extra(self):
        meta = json.loads((TESTDATA / "new-metadata.json").read_text())
        fields = self.pr.common_fields(self.tmp, meta)
        self.assertEqual(fields["images"]["upload"]["digest"], "sha256:" + "b" * 64)
        self.assertEqual(fields["extra"], {"run_id": "main-20261001t120312z"})
        old = self.pr.common_fields(self.tmp, {"label": "x", "started_at_utc": "t"})
        self.assertEqual((old["images"], old["extra"]), ({}, None))


if __name__ == "__main__":
    unittest.main()
