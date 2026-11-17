"""Tests for check-image-age.py.

Run by check_image_age_test.go, and on its own with:

    cd scripts && python3 -m unittest test_check_image_age
"""

from __future__ import annotations

import datetime
import importlib.util
import os
import pathlib
import unittest

_SPEC = importlib.util.spec_from_file_location(
    "check_image_age", pathlib.Path(__file__).parent / "check-image-age.py"
)
check = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(check)


class ParseBuilt(unittest.TestCase):
    """Docker's timestamps, which are not all fromisoformat accepts."""

    def test_nanoseconds(self):
        # What docker actually prints. fromisoformat only takes this from
        # python 3.11, and the system python on macOS is 3.9: parsing that
        # failed quietly would make the whole check a no-op.
        built = check.parse_built("2026-09-24T18:16:16.353297749Z")
        self.assertEqual(
            built,
            datetime.datetime(2026, 9, 24, 18, 16, 16, 353297, tzinfo=datetime.timezone.utc),
        )

    def test_microseconds(self):
        built = check.parse_built("2026-09-24T18:16:16.353297Z")
        self.assertIsNotNone(built)
        self.assertEqual(built.tzinfo, datetime.timezone.utc)

    def test_whole_seconds(self):
        self.assertIsNotNone(check.parse_built("2026-09-24T18:16:16Z"))

    def test_not_a_time(self):
        self.assertIsNone(check.parse_built(""))
        self.assertIsNone(check.parse_built("never"))


class SplitTag(unittest.TestCase):
    def test_ordinary(self):
        self.assertEqual(check.split_tag("ghcr.io/fil-forge/ingot:main"), ("ghcr.io/fil-forge/ingot", "main"))

    def test_no_tag_means_latest(self):
        self.assertEqual(check.split_tag("alpine"), ("alpine", "latest"))

    def test_registry_port_is_not_a_tag(self):
        # The colon in a host:port is followed by a slash; a tag's is not.
        self.assertEqual(check.split_tag("localhost:5000/piri"), ("localhost:5000/piri", "latest"))

    def test_registry_port_with_a_tag(self):
        self.assertEqual(check.split_tag("localhost:5000/piri:main"), ("localhost:5000/piri", "main"))


class MovingRefs(unittest.TestCase):
    """Only a tag that moves can go stale."""

    def test_keeps_moving_tags_and_drops_pinned_ones(self):
        refs = [
            "ghcr.io/fil-forge/ingot:main",
            "ghcr.io/fil-forge/guppy:main-dev",
            "openbao/openbao:2.6",
            "rnwood/smtp4dev:v3",
            "amazon/dynamodb-local:latest",
            "ghcr.io/fil-forge/minio:RELEASE.2025-10-15T17-29-55Z",
        ]
        self.assertEqual(
            check.moving_refs(refs, check.moving_tags()),
            [
                "ghcr.io/fil-forge/ingot:main",
                "ghcr.io/fil-forge/guppy:main-dev",
                "amazon/dynamodb-local:latest",
            ],
        )

    def test_drops_repeats(self):
        # compose config --images prints one line per service, so a shared
        # image appears many times.
        refs = ["openbao/openbao:2.6", "ghcr.io/fil-forge/piri:main", "ghcr.io/fil-forge/piri:main"]
        self.assertEqual(check.moving_refs(refs, check.moving_tags()), ["ghcr.io/fil-forge/piri:main"])


class ParseRefs(unittest.TestCase):
    def test_one_per_line(self):
        self.assertEqual(check.parse_refs("a:main\nb:main\n"), ["a:main", "b:main"])

    def test_blank_lines_and_padding(self):
        self.assertEqual(check.parse_refs("\n  a:main  \n\n"), ["a:main"])

    def test_nothing(self):
        self.assertEqual(check.parse_refs(""), [])


class WarnDays(unittest.TestCase):
    def setUp(self):
        self.addCleanup(os.environ.pop, "SMELT_IMAGE_AGE_WARN_DAYS", None)

    def test_default(self):
        os.environ.pop("SMELT_IMAGE_AGE_WARN_DAYS", None)
        self.assertEqual(check.warn_days(), check.DEFAULT_WARN_DAYS)

    def test_zero_turns_it_off(self):
        os.environ["SMELT_IMAGE_AGE_WARN_DAYS"] = "0"
        self.assertEqual(check.warn_days(), 0)

    def test_a_value_that_is_not_a_count_turns_it_off_rather_than_failing(self):
        os.environ["SMELT_IMAGE_AGE_WARN_DAYS"] = "a fortnight"
        self.assertEqual(check.warn_days(), 0)


class Report(unittest.TestCase):
    def test_names_the_oldest_and_counts_the_rest(self):
        aged = [(d, f"img{d}:main") for d in range(100, 93, -1)]
        out = check.report(aged, total=12, days=14)
        self.assertIn("7 of 12 images", out)
        self.assertIn("img100:main", out)
        self.assertIn("(+2 more)", out)
        self.assertIn("make pull", out)


if __name__ == "__main__":
    unittest.main()


class IsStale(unittest.TestCase):
    """The boundary, which a whole-day comparison got wrong by up to a day."""

    def test_just_past_the_threshold(self):
        # Truncating this to whole days gives 14, which is not more than 14,
        # so the warning used to wait until the next day.
        self.assertTrue(check.is_stale(datetime.timedelta(days=14, hours=23), 14))

    def test_just_short_of_it(self):
        self.assertFalse(check.is_stale(datetime.timedelta(days=13, hours=23), 14))

    def test_exactly_the_threshold_is_not_more_than_it(self):
        self.assertFalse(check.is_stale(datetime.timedelta(days=14), 14))

    def test_well_past_it(self):
        self.assertTrue(check.is_stale(datetime.timedelta(days=194), 14))
