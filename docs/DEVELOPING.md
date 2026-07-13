# Developing Against Sibling Service Repos

Smelt normally runs **published** images for every service. When you're changing the code
of a service — or the shared `libforge` library — and want to validate it in the full stack,
use a **Go workspace** (`go.work`) plus the `SMELT_WORKSPACE=1` flag.

## What this gives you

With the flag set, smelt:

1. reads the active `go.work` to decide which services you're editing,
2. compiles each from your local checkout into a static `linux/amd64` binary (the workspace
   bakes in your cross-module edits, including a local `libforge`), and
3. bind-mounts each binary over the binary in the otherwise-**published** image.

So the published image still provides the runtime (base OS, certs, side tools like guppy's
`randdir`); only the one binary you changed is swapped in. There are **no Dockerfiles to
maintain, no image rebuilds, and no `go.work` inside Docker** — the compile happens on the host
where the workspace already resolves local source. A plain `make up` (no flag) runs pure
published images, exactly as before.

## Sibling layout

`go.work` lives at the `fil-forge/` parent — *above* every repo — so Go's upward search finds
it from any of them. It must list `smelt` plus the repos you're editing:

```
~/workspace/src/github.com/fil-forge/
├── go.work                 # the workspace file (local-only; see below)
├── smelt/                  # this repo
├── libforge/               # shared library
├── piri/               # piri storage node
├── sprue/                  # upload service
├── indexing-service/       # indexer
├── delegator/              # delegator
├── piri-signing-service/   # signing-service
└── guppy/                  # CLI client
```

## 1. Create the workspace (once)

```bash
cd ~/workspace/src/github.com/fil-forge
go work init ./smelt ./piri        # list smelt + whatever you're editing
```

`go.work` is local-only — it sits above every git repo and is conventionally gitignored, so it
is never committed. `rm go.work` reverts everything to the pinned module versions. The same
file also makes your editor and `go test` resolve the local sibling source, so there are no
`replace` directives to add to any `go.mod`.

The `use`-list is the **single source of truth** for what gets rebuilt.

### Including guppy or ingot: the genproto replace

If you put `./guppy` (or `./ingot`) in the `use`-list, the build fails with an *ambiguous import*
for `google.golang.org/genproto/googleapis/{rpc,api}/...`: those modules pull the **pre-split
monolithic** `google.golang.org/genproto` (≈2021) transitively, and its `rpc`/`api` packages
collide with the **split** genproto modules smelt requires. Add a workspace-local `replace` that
bumps the monolith to a post-split version (where those packages are delegated to the split
modules):

```
go 1.26.1

use (
	./smelt
	./guppy
	./ingot
)

replace google.golang.org/genproto => google.golang.org/genproto v0.0.0-20260526163538-3dc84a4a5aaa
```

This lives only in `go.work` (gitignored), so it never touches any committed `go.mod`. Other
siblings (piri-pdp, sprue, …) don't need it.

> Ingot's forge e2e tests live in the **ingot repo** (`testing/forge_*_test.go`), not in smelt
> — see "Service repos own their e2e tests" below. Put `./ingot` in the `use`-list (plus the
> replace above) only when you want `SMELT_WORKSPACE=1` smelt runs to rebuild ingot from local
> source.

### Selection and the `libforge` rule

A service is rebuilt from local source when its module dir is in the `use`-list. Because the
siblings resolve `libforge` *only* through the workspace, **listing `libforge` forces all six
services to rebuild** — a published binary would otherwise still link the published `libforge`.

| Editing… | put in `go.work` | smelt rebuilds |
|---|---|---|
| just piri | `./smelt ./piri` | piri |
| upload + indexer | `./smelt ./sprue ./indexing-service` | upload, indexer |
| the shared lib | `./smelt ./libforge` | **all six services** |

Module → service → container binary (defined in `pkg/workspace`):

| module dir | service | container binary |
|---|---|---|
| `piri` | piri (every piri-N) | `/usr/bin/piri` |
| `sprue` | upload | `/usr/bin/sprue` |
| `piri-signing-service` | signing-service | `/usr/bin/signer` |
| `indexing-service` | indexer | `/usr/bin/indexer` |
| `delegator` | delegator | `/usr/bin/registrar` |
| `guppy` | guppy | `/usr/bin/guppy` |

## 2. Run with local binaries

```bash
SMELT_WORKSPACE=1 make up        # compile selected services + mount over images + start
SMELT_WORKSPACE=1 make fresh     # same, from a clean slate

# In the Go test stack (the e2e suite is behind the `e2e` build tag):
SMELT_WORKSPACE=1 go test -tags e2e ./tests/e2e -run TestUploadAndRetrieve
```

(Run from inside `smelt/` so `go.work` is picked up — verify with `go env GOWORK`.)

## How it works

`SMELT_WORKSPACE=1` makes the Makefile run `smelt workspace build`, which:

- compiles each selected service into `generated/bin/<service>`, and
- writes `generated/compose/workspace.override.yml` — a compose override that mounts each
  binary `:ro` at its container path (the piri entry fans out to every `piri-N` node).

That override is chained into every `$(COMPOSE)` call while the file exists. A plain `make up`
deletes it, so you fall back to published images. (`make clean` / `make nuke` also remove it.)

In the Go test stack the same machinery runs via `stack.WithWorkspaceBinaries()`; the e2e test
turns it on when `SMELT_WORKSPACE=1` is set. To inject a specific prebuilt binary without the
workspace, use `stack.WithServiceBinary("upload", "/path/to/sprue")`.

## Fast per-edit loop

Once the stack is up with `SMELT_WORKSPACE=1`, you don't need to re-boot it for a one-line
change — rebuild just the affected service's binary and restart its container. The rest of the
stack (and the chain state) stays up, so you skip the slow contract-deploy + registration boot:

```bash
docker compose stop piri-0              # + piri-1 piri-2 … for multi-node setups
SMELT_WORKSPACE=1 make workspace-build  # recompiles selected services into generated/bin/
docker compose start piri-0
```

Stop the container **first**: its `/usr/bin/piri` is the bind-mounted binary that's currently
executing, and rebuilding over an executing file fails with `text file busy` (`ETXTBSY`).
Stopping releases it; starting re-execs the freshly built binary.

Keep the `use`-list narrow (e.g. just `./smelt ./piri`) so `workspace-build` only recompiles
what you're working on.

## Turning it off / troubleshooting

- **Back to published images:** run any `make` target *without* `SMELT_WORKSPACE=1` (it removes
  the override), or `rm go.work` to also drop local resolution for your editor.
- **Build/selection surprises:** `go env GOWORK` shows the active workspace; if it's empty,
  `SMELT_WORKSPACE=1` will error asking you to `go work init`. Confirm every sibling in the
  `use`-list actually exists on disk.
- **A service won't come up healthy:** that's your local code running — check its logs
  (`docker compose logs -f <service>`). The injection is working; the failure is in the binary
  you just built.

## Service repos own their e2e tests (smelt as SDK)

The workspace flow above is for validating changes *inside smelt runs*. The complementary —
and for day-to-day service development, preferred — direction is the service repo importing
**smelt as a Go test dependency** and orchestrating the stack from its own e2e tests:

```go
s := stack.MustNewStack(t,
    stack.WithPiriNodes(stack.PiriNodeConfig{}),
    // Mount the working tree's binary over the published image. Build it
    // yourself (CGO_ENABLED=0 GOOS=linux GOARCH=<host arch>); see ingot's
    // testing/stack_e2e_test.go for a sync.Once build helper.
    stack.WithServiceBinary("ingot", localBinary),
    // Optional: exercise a config change without a smelt release.
    stack.WithServiceConfig("ingot", "testdata/config.yaml"),
)
endpoint := s.IngotEndpoint()
```

Everything travels with the Go import — compose files, per-service configs, and embedded
snapshots ride along via `go:embed`, so no smelt checkout is required. Add a `TestMain` that
calls `stack.CleanupLeaked` to sweep `smeltery-*` leftovers from crashed runs, and
`guppy.LoginViaEmail` for flows that need a logged-in agent.

> **One e2e run per host at a time.** `CleanupLeaked` sweeps *every* `smeltery-*` container,
> including live ones — concurrently starting another repo's e2e suite on the same Docker
> host will tear the first run's stacks out from under it mid-boot.

**Division of responsibility:** smelt owns each service's *system definition* (compose
topology, ports, default config, key wiring) and asserts it boots healthy; the service repo
owns its *behavior* tests. Ingot is the reference implementation of this pattern
(`ingot/testing/forge_*_test.go` + its `e2e` CI workflow); new services should follow it
rather than adding behavior tests to smelt's `tests/e2e/`.
