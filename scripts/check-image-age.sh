#!/usr/bin/env bash
# Warn when the stack is about to run images that are weeks old.
#
# Compose pulls a tag once and then keeps serving what it cached, so a stack
# can run a `:main` from months ago with every container reporting healthy.
# Nothing about the symptom says "old image": it arrives as a plausible error
# in the service's own vocabulary, so the hunt starts in the wrong repo.
# `make fresh` does not help either, because `down --rmi local` keeps every
# image carrying a tag of its own, which is all the published ones.
#
# Only moving tags are reported. A pinned one (openbao/openbao:2.6,
# rnwood/smtp4dev:v3) names the exact build it wants, so its age says nothing
# and pulling would change nothing.
#
# Advisory only: it never fails a target, because an old image is often what
# an offline or deliberately pinned run wants.
#
# SMELT_IMAGE_AGE_WARN_DAYS sets the threshold in days (default 14); 0 turns
# the check off. SMELT_MOVING_TAGS overrides the tag list.

set -uo pipefail

command -v docker >/dev/null 2>&1 || exit 0
command -v python3 >/dev/null 2>&1 || exit 0

compose=("$@")
[ "${#compose[@]}" -gt 0 ] || compose=(docker compose)

images="$("${compose[@]}" images --format json 2>/dev/null)" || exit 0
[ -n "$images" ] || exit 0

# docker image inspect is the only source of a build date, and it takes one
# reference at a time, so the ages are gathered here and the reporting is
# left to python.
printf '%s' "$images" | python3 -c '
import datetime, json, os, subprocess, sys

days = os.environ.get("SMELT_IMAGE_AGE_WARN_DAYS", "14").strip()
if not days.isdigit() or int(days) == 0:
    sys.exit(0)
days = int(days)

moving = {
    t.strip()
    for t in os.environ.get("SMELT_MOVING_TAGS", "main,main-dev,latest,edge").split(",")
    if t.strip()
}

raw = sys.stdin.read().strip()
try:
    rows = json.loads(raw)
    if isinstance(rows, dict):
        rows = [rows]
except json.JSONDecodeError:
    rows = []
    for line in raw.splitlines():
        line = line.strip()
        if line:
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                pass

refs, seen = [], set()
for row in rows:
    repo, tag = row.get("Repository") or "", row.get("Tag") or ""
    # No repository means an image built here and never tagged.
    if not repo or tag not in moving:
        continue
    ref = f"{repo}:{tag}"
    if ref not in seen:
        seen.add(ref)
        refs.append(ref)

now = datetime.datetime.now(datetime.timezone.utc)
aged = []
for ref in refs:
    try:
        created = subprocess.run(
            ["docker", "image", "inspect", ref, "--format", "{{.Created}}"],
            capture_output=True, text=True, timeout=10,
        ).stdout.strip()
        built = datetime.datetime.fromisoformat(created.replace("Z", "+00:00"))
    except Exception:
        continue
    age = (now - built).days
    if age > days:
        aged.append((age, ref))

if not aged:
    sys.exit(0)

aged.sort(reverse=True)
shown = aged[:5]
print()
print(f"WARNING: {len(aged)} of {len(refs)} images on a moving tag are more than {days} days old.")
for age, ref in shown:
    print(f"  {ref:<52} {age} days")
if len(aged) > len(shown):
    print(f"  (+{len(aged) - len(shown)} more)")
print()
print("  Compose keeps a tag it has already pulled. Refresh them with:")
print("    make pull")
print("  Silence this with SMELT_IMAGE_AGE_WARN_DAYS=0.")
print()
' 2>/dev/null

exit 0
