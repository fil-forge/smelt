# Smelt UCAN1 Integration — Handoff

**Author:** Forrest (with Claude, session 2026-05-27)
**Audience:** Whoever is picking up the smelt + UCAN1 migration next.
**Status:** End-to-end `guppy upload` + `guppy retrieve` works on the
integration branch listed below. Diagnostic logging and several follow-up
issues are intentionally left in place — see [§7 Cleanup before merging
upstream](#7-cleanup-before-merging-upstream) and [§5 Open issues to file](#5-open-issues-to-file).

> **For the next AI session reading this doc:** the previous session
> diagnosed and fixed ~11 distinct bugs across 5 repos. This document
> is your primary context — read it top to bottom before touching code.
> The fragile spots are flagged, and several of the fixes are explicitly
> *workarounds* with TODOs pointing at upstream issues.

---

## 1. TL;DR

A working end-to-end upload flow exists on `frrist/smelt/ucan1-integration`
in four sibling repos plus `frrist/fix/proof-chain` in libforge. Smelt
itself (this repo) is on `frrist/dev/ucan1`. Everything is pushed.

To reproduce the working state:

```bash
# Sibling layout
~/code/fil-forge/
├── smelt/             # branch frrist/dev/ucan1
├── libforge/          # branch frrist/fix/proof-chain
├── piri-pdp/          # branch frrist/smelt/ucan1-integration
├── sprue/             # branch frrist/smelt/ucan1-integration
├── indexing-service/  # branch frrist/smelt/ucan1-integration
└── guppy/             # branch frrist/smelt/ucan1-integration

cd smelt
SMELT_DEV=1 make fresh
```

Then run the guppy upload flow described in [§6 Verification](#6-verification).

---

## 2. Repos and branches

| Repo | Branch | Tip commit | Notes |
|---|---|---|---|
| smelt | `frrist/dev/ucan1` | `f425a9a fix: generate ID for guppy and configure in container` | clean, no pending changes |
| libforge | `frrist/fix/proof-chain` | `707e595 fix: proof chain terminates powerline delegations` | branched from main |
| piri-pdp | `frrist/smelt/ucan1-integration` | `ec27064 wip: ucan1 integration snapshot for smelt e2e` | branched from `frrist/ucan1` |
| sprue | `frrist/smelt/ucan1-integration` | `6b0e9c1 wip: ucan1 integration snapshot for smelt e2e` | branched from `ash/feat/ucan1`; libforge bumped to `707e595` |
| indexing-service | `frrist/smelt/ucan1-integration` | `b88adc5 wip: ucan1 integration snapshot for smelt e2e` | branched from `ash/feat/ucan-1` |
| guppy | `frrist/smelt/ucan1-integration` | `71c7776 wip: ucan1 integration snapshot for smelt e2e` | branched from `ash/feat/client-upgrade` (which is 27 ahead / 26 behind upstream — see §8) |

All five branches/commits are pushed to origin.

The smelt working tree uses `SMELT_DEV=1` to build container images from the
sibling checkouts via `compose.dev.yml` + the piri generator's inlined
`build:` block. There are no smelt-side changes required to consume the
integration branches.

---

## 3. Build + smoke-test ritual

```bash
# (Optional, first time) ensure all the siblings exist next to smelt
cd ~/code/fil-forge
for r in libforge piri-pdp sprue indexing-service guppy; do
  [ -d "$r" ] || git clone git@github.com:fil-forge/$r.git
done

# Check out the right branch in each
git -C libforge          checkout frrist/fix/proof-chain
git -C piri-pdp          checkout frrist/smelt/ucan1-integration
git -C sprue             checkout frrist/smelt/ucan1-integration
git -C indexing-service  checkout frrist/smelt/ucan1-integration
git -C guppy             checkout frrist/smelt/ucan1-integration

# Build the dev images + bring the stack up
cd smelt
SMELT_DEV=1 make fresh         # nuke + build + start
make status                    # all services should be healthy
```

If `make fresh` fails resolving the libforge pseudo-version, your local
libforge branch isn't pushed (or you forgot `SMELT_DEV=1` and pulled
stale published images). Confirm `git -C libforge log -1` shows `707e595`
and that `git -C libforge branch -vv` shows it tracking origin.

---

## 4. Per-repo change summary

The session walked the full upload flow and peeled bugs layer by layer.
Each fix below has a one-line "why," and the file path it lives in. The
**piri-pdp DID resolver fix** is the breakthrough — everything before
that point was peeling layers until the real failure mode surfaced.

### libforge — `frrist/fix/proof-chain`

- **`commands/blob/log.go`** (untracked sibling file, not committed) +
  the single committed fix `707e595`: proof chain terminates powerline
  delegations. Previously a chain walk could over-shoot the root and
  treat unrelated parent delegations as proofs. Affects everywhere
  `ProofChain` / chain-walking is consumed.

### piri-pdp — `frrist/smelt/ucan1-integration`

- **`pkg/ucanhandlers/ucanfx/fx.go`** — THE BREAKTHROUGH. The retrieval
  server's fx wiring was missing `WithDIDVerifierResolvers(...)`. Without
  it, every retrieval invocation from a `did:web:*` issuer failed
  validation with "unsupported DID method: web". The failure receipt has
  no HTTP metadata, so the retrieval server falls through to a default
  200 OK / empty body / failure-in-`X-UCAN-Container` response that
  downstream (`blobindexlookup`) silently decodes as "empty CAR." That
  manifested as `decoding index CAR: EOF` two layers up. The fix copies
  the `ProvideRPCOption` block as `ProvideRetrievalOption` so both
  transports get the same resolver map. See the comment in the file for
  the full backstory.

- **`pkg/config/services.go`** — `orderProofChain` helper. UCAN containers
  byte-sort their tokens; UCAN invocation `prf` chains are strictly
  ordered root→leaf. When piri reads a container, it gets bag-of-tokens
  ordering and re-feeds them to the validator, which then complains
  "delegation issuer is X, not Y." `orderProofChain` walks the
  delegations, finds the root (issuer that nobody audiences), and
  bubble-sorts into chain order. **This is a workaround**, not a real
  fix — the TODO in the file references the upstream ucantone issue
  (see §5).

- **`pkg/ucanhandlers/blob/retrieve.go`** — `errorResponse` renamed to
  exported `ErrorResponse` so `content/retrieve.go` can reuse it. The
  function's contract is documented in a comment: *callers must call
  `SetMetadata(container)` AND `SetFailure(err)`; setting only the
  failure leaves the HTTP response a misleading 200 OK with empty body.*

- **`pkg/ucanhandlers/content/retrieve.go`** — NotAllocated path now
  uses `ErrorResponse(http.StatusNotFound, NotAllocatedErrorName, msg)`
  plus `rsp.SetMetadata` plus `rsp.SetFailure`. Was previously just
  `SetFailure`, which (per the bug above) renders as a 200/empty.
  ⚠️ **Still contains diagnostic logging** — see §7.

- **`cmd/cli/{root,setup/register}.go`, `pkg/config/app/services.go`,
  `pkg/service/{egresstracker,publisher/{options,publisher}}.go`,
  `go.{mod,sum}`** — pre-existing Forrest work on the parent branch,
  carried over. Not strictly part of the session debugging, but lives
  on the same integration branch.

### sprue — `frrist/smelt/ucan1-integration`

- **`pkg/service/handlers/ucan_conclude_http_put.go`** — when sprue
  forwards the upload completion to piri's `blob/accept`, it has to
  reconstruct the exact same invocation that `blob_add.go` originally
  sent piri for `blob/allocate`. The CID has to match byte-for-byte
  because piri keys its receipt store by it. Two fixes here: (1) use
  `putInv.Task().Link()` (not `putInv.Link()`) to get the inner-task
  CID that piri stored; (2) add `invocation.WithNoNonce()` so the
  determinism matches what `blob_add.go` builds. Symptom before the
  fix: `polling accept receipt: receipt for X was not found after 6 attempts`.

- **`pkg/service/handlers/blob_add.go`** — added `acceptExtras` struct
  to forward piri's accept-receipt metadata (location commitment +
  `/pdp/accept` receipt) into the response so guppy gets the full chain.

- **`pkg/service/handlers/access_confirm.go`, `pkg/service/service.go`,
  `go.{mod,sum}`** — Ash's earlier work; go.mod bump to libforge 707e595
  done in this branch for cross-repo consistency.

### indexing-service — `frrist/smelt/ucan1-integration`

- **`pkg/service/service.go`** —
  - `urlForResource` normalizes `url.Path` to ensure a leading `/`.
    Multiaddr → URL conversion via `maurl.ToURL` produces a
    path-segment-based URL where `/` can be missing; downstream HTTP
    routing fails silently.
  - **Two `len(proofs) == 0` guards** added to bracket calls to
    `proofStore.ProofChain(...)`. `ProofChain` can return `(nil, nil)`
    (no error, no chain) and the surrounding code did `proofs[0].Subject()`
    unconditionally → runtime panic. One guard is in
    `extractContentRetrieveDelegation`, the other is in `jobHandler` near
    the query-resolution path. Both panics were tripped during this session.

- **`pkg/service/contentclaims/ucanservice.go`** — assert/equals and
  assert/index routes now `return rsp.SetFailure(err)` instead of
  `return err`. Returning a raw error makes the server respond with
  `text/plain` body, which the UCAN client then chokes on with
  `decoding response: invalid content type "text/plain"`.

- **`pkg/service/blobindexlookup/simplelookup.go`** —
  - leaf delegation now flows through to the retrieval invocation
    (paired with the guppy `WithDelegations` fix below).
  - ⚠️ **Diagnostic logging still in place** — see §7.

- **`cmd/server.go`, `go.{mod,sum}`** — Ash's earlier work.

### guppy — `frrist/smelt/ucan1-integration`

- **`pkg/client/indexadd.go`** — adds
  `execution.WithDelegations(retrievalAuth)` to the `Execute()` call
  for `space/index/add`. Without this, the leaf delegation (the
  `/content/retrieve` UCAN that piri needs the indexer to forward to
  retrievers) gets referenced by CID in the invocation's `prf` chain
  but never actually shipped over the wire, so the indexer can't
  resolve it. Symptom: indexer returned a partial response that
  downstream couldn't decode.

  ⚠️ *Possible double-fix*: it's plausible that the server *should* be
  pulling delegations out of the container, and this client-side
  `WithDelegations` is papering over a server-side bug. Worth a closer
  look during the upstream cleanup.

- **`pkg/client/{accounts,claimaccess,spaces}.go`, `go.{mod,sum}`** —
  Ash's earlier work.

---

## 5. Open issues to file

These are bugs surfaced during the session but not yet fixed (or fixed
only as workarounds). Each one is worth a GitHub issue so it doesn't
get lost.

1. **ucantone: container token ordering vs invocation prf ordering**
   — UCAN-WG container spec says bytewise-sorted; invocation prf is
   strictly ordered root→leaf. The validator currently requires the
   prf order. piri's `orderProofChain` is a workaround. Real fix is
   either spec clarification or making the validator tolerant of
   container ordering. piri-pdp `pkg/config/services.go` has a TODO
   comment referencing ucantone#29 — **verify that issue actually
   exists**; if not, file it.

2. **libforge `ProofChain` returns `(nil, nil)` instead of explicit
   `ErrChainNotFound`** — caused two distinct panics in
   indexing-service during this session. Both were patched with
   `len(proofs) == 0` guards at the call sites, but the root cause
   is that the API encourages misuse. Should return an explicit
   sentinel error so the type system forces callers to handle the
   "no chain" case.

3. **piri-pdp: `ucanhandlers/*` loggers absent from INFO-bumped list**
   — `cmd/cli/root.go:122-143` has a hand-curated list of loggers to
   bump from default WARN to INFO. The new `ucanhandlers/content` and
   `ucanhandlers/blob` (and any other `ucanhandlers/*`) loggers are
   not in the list. Took a chunk of debug time this session because
   "I don't see the logs in piri" had nothing to do with the code
   path being wrong and everything to do with filtered logging.

4. **piri-pdp: `ProvideRPCOption` vs `ProvideRetrievalOption` duplication**
   — the fix in `ucanfx/fx.go` exposes a structural problem: the two
   server-option providers are parallel, and forgetting one of them
   (as happened with `ProvideRetrievalOption` for the DID resolver)
   silently breaks the retrieval transport. Worth either consolidating
   into a single "server options" provider that applies to both, or
   at minimum a checklist comment + linter that catches the divergence.

5. **sprue: `blob_add` fast-path trusts partial state from prior
   failed attempts** — if a previous upload attempt got partway through
   and left state, `blob_add.go`'s fast-path can short-circuit and
   skip work that's actually necessary. Manifested during this session
   as flaky upload retries. Worth a deeper look at the idempotency
   contract.

6. **guppy↔piri: chunked-PUT truncation on ~250MB+ shards** — intermittent
   (~50% of attempts) the HTTP PUT from guppy to piri loses bytes
   somewhere in the chunked-encoding path. Smaller shards (the default
   10KB test data) never trip it. This session didn't get a clean
   repro narrowing it to client vs. server. Probably a Go `http.Client`
   transport setting on guppy's side or an echo body-reader behavior
   on piri's side. **Fail-fast guards in piri/sprue would surface this
   earlier instead of letting it cascade into CAR-decode failures.**

---

## 6. Verification

After `SMELT_DEV=1 make fresh && make status` shows everything healthy:

```bash
make shell-guppy
# inside container:
guppy login test@example.com
export SPACE=$(guppy space generate)
echo "Space: $SPACE"

# Generate 10KB test data (uploads require minimum 1KB)
randdir --size 10KB --output /tmp/test-data

# Add the source to the space (does not upload yet)
guppy upload source add $SPACE /tmp/test-data

# Upload
guppy upload $SPACE
# Expected: "Upload completed successfully: bafy..."

# Retrieve (verify round-trip)
guppy retrieve $SPACE <CID-from-above> /tmp/retrieved
ls -la /tmp/retrieved   # files should match /tmp/test-data
```

Logs to check if something goes wrong:

```bash
docker compose logs -f piri        # storage node
docker compose logs -f upload      # sprue
docker compose logs -f indexer     # indexing-service
docker compose logs -f delegator   # UCAN delegation issuance
```

If you need debug logs in piri, the level is controlled via the CLI in
`piri-pdp/cmd/cli/client/admin/log/root.go`. Note caveat in §5.3 about
`ucanhandlers/*` loggers not being in the default INFO list.

---

## 7. Cleanup before merging upstream

These two files have diagnostic logging that was deliberately left in
for the integration snapshot. **Before any of this can be merged to
the parent feature branches (`frrist/ucan1`, `ash/feat/ucan1`,
`ash/feat/ucan-1`, `ash/feat/client-upgrade`), strip them out:**

### `piri-pdp/pkg/ucanhandlers/content/retrieve.go`

Remove:
- The `fmt.Fprintf(os.Stderr, "STDERR_DIAG ...")` block in the handler body
- Both `log.Warnw("content/retrieve invoked", ...)` and
  `log.Warnw("content/retrieve result", ...)` calls
- `var log = logging.Logger("ucanhandlers/content")` at top of file
- `os` and `logging "github.com/ipfs/go-log/v2"` imports

The NotAllocated `ErrorResponse` + `SetMetadata` + `SetFailure` change
is the keeper; just rip the loud diagnostics.

### `indexing-service/pkg/service/blobindexlookup/simplelookup.go`

Remove:
- All three `log.Warnw` blocks (`"authorized retrieval target"`,
  `"retrieval receipt outcome"` + `"retrieval receipt failure"`,
  `"authorized retrieval response"`)
- The body-buffering block at the bottom (`all, readErr := io.ReadAll`
  + `io.NopCloser(bytes.NewReader(all))`) — revert to returning
  `meta.Body` directly
- `var log = logging.Logger("blobindexlookup")` at top of file
- `bytes`, `io`-related extras, and `logging "github.com/ipfs/go-log/v2"`
  imports that only the diagnostics need (keep what the leaf-delegation
  fix requires)

The leaf-delegation forwarding (`execution.WithDelegations(...)` and the
prfLinks loop) is the keeper.

---

## 8. Known fragility

1. **guppy parent branch divergence** — `ash/feat/client-upgrade` is
   27 commits ahead / 26 behind upstream `origin/ash/feat/client-upgrade`.
   `frrist/smelt/ucan1-integration` was branched off the diverged state.
   This is fine for a snapshot, but **do not try to fast-forward merge
   this branch back into upstream without first rebasing**. The right
   workflow is probably: cherry-pick the one functional commit
   (`71c7776`) onto a rebased branch.

2. **piri-pdp `orderProofChain` is a workaround** — if/when ucantone
   fixes its container/prf ordering story, delete this helper and call
   the validator directly. The TODO in the file points to the upstream
   issue.

3. **guppy `WithDelegations` may be papering over a server bug** —
   see §4 (guppy section). If you find time, trace whether the indexer
   *should* be pulling the leaf out of the invocation container on its
   own.

4. **sprue libforge bumped without test run** — bumped from `4361ce6`
   to `707e595` in this branch for consistency. Build passes; the e2e
   passes. No sprue-internal test suite was run against the new pin.
   Low risk (the diff is only the proof-chain fix, which sprue doesn't
   directly invoke) but flagged for completeness.

---

## 9. Suggested next moves for a fresh session

In rough priority order:

1. **File the issues in §5.** They're the right place to capture this
   before context evaporates. ucantone#29 first — verify it exists,
   file if it doesn't; everything else can follow.

2. **Strip the diagnostic logging in §7.** Quick win, gets the
   integration branch closer to mergeable shape.

3. **Add fail-fast guards for §5.6** (the chunked-PUT truncation). Even
   without root-causing the truncation, surfacing it earlier in piri or
   sprue (assert content-length matches expected, fail the receipt
   with a meaningful error) would convert "mystery CAR decode EOF" into
   a clear "uploaded 247MB of expected 250MB" failure.

4. **Decide on `orderProofChain`'s long-term home.** Either accept
   the workaround and document it, or push the fix into ucantone and
   delete the piri-side helper.

5. **Rebase guppy onto upstream `ash/feat/client-upgrade`.** Will
   need to handle the divergence — see §8.1.

6. **Snapshot the working stack with `./smelt snapshot save ucan1-baseline`**
   so future cold-boots don't pay the 1-minute registration cost.
   (Optional; speeds iteration but not required.)

---

## 10. Session artifacts

- Plan file for the branch-cut work: `~/.claude/plans/floofy-toasting-aurora.md`
- Full transcript of the session that produced this state:
  `~/.claude/projects/-home-frrist-workspace-src-github-com-fil-forge-smelt/6472c5c5-427a-4f7f-a39b-620067339591.jsonl`

If you want to load context into a fresh Claude session, the canonical
path is: ask Claude to read this file (`smelt/docs/HANDOFF.md`) first,
then point at whichever section corresponds to what you're picking up.
