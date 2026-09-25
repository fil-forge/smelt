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
