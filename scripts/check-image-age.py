#!/usr/bin/env python3
"""Warn when the stack is about to run images that are weeks old.

Compose pulls a tag once and then keeps serving what it cached, so a stack can
run a `:main` from months ago with every container reporting healthy. Nothing
about the symptom says "old image": it arrives as a plausible error in the
service's own vocabulary, so the hunt starts in the wrong repository.
`make fresh` does not help either, because `down --rmi local` keeps every
image carrying a tag of its own, which is all the published ones.

Only moving tags are reported. A pinned one (openbao/openbao:2.6,
rnwood/smtp4dev:v3) names the exact build it wants, so its age says nothing
and pulling would change nothing.

Advisory only: it exits 0 whatever happens, because an old image is often what
an offline or deliberately pinned run wants, and this must never be the reason
a stack refuses to start.

The references come from `compose config --images`, which reads the
configuration. `compose images` would read the project's existing containers
instead, so it answers nothing at all on a stack that is down, which is
exactly when this runs.

Usage: check-image-age.py [compose command...]

    SMELT_IMAGE_AGE_WARN_DAYS   threshold in days (default 14); 0 turns it off
    SMELT_MOVING_TAGS           comma-separated tags to treat as moving
"""

from __future__ import annotations

import datetime
import json
import os
import re
import subprocess
import sys

DEFAULT_WARN_DAYS = 14
DEFAULT_MOVING_TAGS = "main,main-dev,latest,edge"
# Enough to name the worst offenders without burying the hint under them.
MAX_LISTED = 5


def warn_days() -> int:
    """The threshold, or 0 when the check is off or the value is not a count."""
    raw = os.environ.get("SMELT_IMAGE_AGE_WARN_DAYS", str(DEFAULT_WARN_DAYS)).strip()
    return int(raw) if raw.isdigit() else 0


def moving_tags() -> set[str]:
    raw = os.environ.get("SMELT_MOVING_TAGS", DEFAULT_MOVING_TAGS)
    return {tag.strip() for tag in raw.split(",") if tag.strip()}


def parse_refs(raw: str) -> list[str]:
    """The image references `compose config --images` printed, one per line."""
    return [line.strip() for line in raw.splitlines() if line.strip()]


def split_tag(ref: str) -> tuple[str, str]:
    """A reference's repository and tag.

    The tag follows the last colon, unless that colon is part of a registry's
    host:port, which a later slash gives away. A reference naming no tag means
    latest, as docker reads it.
    """
    head, colon, tail = ref.rpartition(":")
    if not colon or "/" in tail:
        return ref, "latest"
    return head, tail


def moving_refs(refs: list[str], tags: set[str]) -> list[str]:
    """Every reference on a tag that moves, in order, once each."""
    out, seen = [], set()
    for ref in refs:
        _, tag = split_tag(ref)
        if tag not in tags or ref in seen:
            continue
        seen.add(ref)
        out.append(ref)
    return out


# Docker stamps nanoseconds, which fromisoformat only accepts from python
# 3.11. The system python on macOS is older, and a parse that quietly failed
# would turn this whole check into a no-op, so the extra digits are cut.
_SUBSECOND = re.compile(r"(\.\d{6})\d+")


def parse_built(created: str) -> datetime.datetime | None:
    """The build time docker reports, or None when it is not a time."""
    text = _SUBSECOND.sub(r"\1", created.strip()).replace("Z", "+00:00")
    try:
        return datetime.datetime.fromisoformat(text)
    except ValueError:
        return None


def image_age_days(ref: str, now: datetime.datetime) -> int | None:
    """How many days ago `ref` was built, or None when docker will not say."""
    try:
        result = subprocess.run(
            ["docker", "image", "inspect", ref, "--format", "{{.Created}}"],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    built = parse_built(result.stdout)
    return None if built is None else (now - built).days


def report(aged: list[tuple[int, str]], total: int, days: int) -> str:
    """The warning, oldest first."""
    lines = [
        "",
        f"WARNING: {len(aged)} of {total} images on a moving tag are more than {days} days old.",
    ]
    for age, ref in aged[:MAX_LISTED]:
        lines.append(f"  {ref:<52} {age} days")
    if len(aged) > MAX_LISTED:
        lines.append(f"  (+{len(aged) - MAX_LISTED} more)")
    lines += [
        "",
        "  Compose keeps a tag it has already pulled. Refresh them with:",
        "    make pull",
        "  Silence this with SMELT_IMAGE_AGE_WARN_DAYS=0.",
        "",
    ]
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    days = warn_days()
    if days == 0:
        return 0

    compose = argv[1:] or ["docker", "compose"]
    try:
        listed = subprocess.run(
            [*compose, "config", "--images"],
            capture_output=True,
            text=True,
            timeout=60,
        )
    except (OSError, subprocess.SubprocessError):
        return 0

    refs = moving_refs(parse_refs(listed.stdout), moving_tags())
    if not refs:
        return 0

    now = datetime.datetime.now(datetime.timezone.utc)
    aged = []
    for ref in refs:
        age = image_age_days(ref, now)
        if age is not None and age > days:
            aged.append((age, ref))
    if aged:
        aged.sort(reverse=True)
        print(report(aged, len(refs), days))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except Exception:  # noqa: BLE001 — advice must never stop a stack starting
        sys.exit(0)
