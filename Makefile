SHELL := /bin/bash
DOCKER := $(shell which docker)

# Set YES=1 to skip confirmation prompts (e.g., make nuke YES=1)
YES ?= 0

# Set SMELT_WORKSPACE=1 to run service containers against binaries built from
# your local sibling checkouts (selected via the active go.work use-list; see
# the "Developing Against Sibling Service Repos" section in CLAUDE.md). When
# enabled, `smelt workspace build` compiles the selected binaries and writes
# generated/compose/workspace.override.yml, which is chained in below so every
# compose call mounts them over the published images.
SMELT_WORKSPACE ?= 0

# Set SMELT_MANIFEST=path/to/manifest.yml to drive the stack off a manifest
# other than the tracked smelt.yml (e.g. manifests/piri-1-postgres-filesystem.yml).
# The Go side reads the same variable (see manifest.ResolveManifestPath), so
# generate, workspace build and snapshot save all follow it; the Makefile only
# needs it to know which file the generated compose depends on.
SMELT_MANIFEST ?=
MANIFEST := $(or $(SMELT_MANIFEST),smelt.yml)
export SMELT_MANIFEST

WORKSPACE_OVERRIDE := generated/compose/workspace.override.yml

# Chain the workspace binary-mount override into every compose call, but only
# when it exists on disk. `workspace-build` (a prereq of up/build/fresh) creates
# it when SMELT_WORKSPACE=1 and removes it otherwise, so a plain `make up` never
# picks up a stale override.
#
# The existence check is a shell command substitution ($$(...)), NOT make's
# $(wildcard)/$(if). workspace-build creates the override mid-recipe (e.g. `up`
# runs it on one line, then `$(COMPOSE) up` on the next), but make expands a
# recipe's functions up front and caches directory listings, so $(wildcard)
# would still report the file absent. Deferring `test -f` to the shell at
# recipe-execution time always reflects what workspace-build just reconciled.
COMPOSE = $(DOCKER) compose $$(test -f $(WORKSPACE_OVERRIDE) && echo "-f compose.yml -f $(WORKSPACE_OVERRIDE)")

# workspace-build reconciles the override to the current SMELT_WORKSPACE value:
# build the selected sibling binaries + write the override when enabled, else
# remove any stale override so published images are used. Prereq of the
# container-starting targets (up / build / fresh).
workspace-build:
	@if [ "$(SMELT_WORKSPACE)" = "1" ]; then \
		go run ./cmd/smelt workspace build; \
	else \
		rm -f $(WORKSPACE_OVERRIDE); \
	fi

.PHONY: help generate init up down restart clean nuke fresh logs pull build cli status guppy regen debug-upload redeploy s3-key ensure-state check-docker workspace-build shell-guppy shell-piri shell-upload shell-hilt

# Default target - show help
help:
	@echo "Forge Compose - Local Development Environment"
	@echo ""
	@echo "Quick Start:"
	@echo "  make up        Start the network and wait until it is healthy"
	@echo "  make down      Stop the network (keeps data)"
	@echo "  make restart   Restart all services"
	@echo ""
	@echo "Lifecycle:"
	@echo "  make generate  Generate compose files and keys from smelt.yml"
	@echo "  make init      Initialize keys, proofs, and Docker network"
	@echo "  make up        Start all services and wait until they are healthy"
	@echo "  make down      Stop all services (preserves data)"
	@echo "  make restart   Stop and start all services"
	@echo "  make clean     Stop + delete volumes (DESTROYS ALL DATA)"
	@echo "  make nuke      Stop + delete volumes + keys + images (DESTROYS EVERYTHING)"
	@echo "  make fresh     Nuke + rebuild + start (DESTROYS EVERYTHING, starts fresh)"
	@echo ""
	@echo "Piri Configuration:"
	@echo "  Edit smelt.yml to configure piri node count and storage backends."
	@echo "  Run 'make generate' (or 'make up') to apply changes."
	@echo "  SMELT_MANIFEST=manifests/<name>.yml make up  Use another manifest"
	@echo "  without editing smelt.yml (ready-made ones live in manifests/)."
	@echo ""
	@echo "Snapshots:"
	@echo "  make cli                          Build the ./smelt CLI binary"
	@echo "  ./smelt snapshot save NAME        Save current stack state"
	@echo "  ./smelt snapshot list             List saved snapshots"
	@echo "  ./smelt snapshot rm NAME          Delete a snapshot"
	@echo "  make up SNAPSHOT=NAME             Boot from a snapshot (or /path/to/snapshot)"
	@echo "  See docs/SNAPSHOTS.md for the full picture"
	@echo ""
	@echo "Development:"
	@echo "  make pull          Pull latest pre-built images"
	@echo "  make build         Build all Docker images"
	@echo "  make regen         Regenerate keys and proofs (requires restart)"
	@echo "  make logs          Follow all service logs"
	@echo "  make status        Show service status"
	@echo "  make shell-guppy   Open shell in guppy container"
	@echo "  make s3-key        Mint an S3 access key and save it as AWS CLI profile"
	@echo "                     'smelt' (TENANT=..., PROFILE=... to override)"
	@echo ""
	@echo "Debugging:"
	@echo "  make debug-upload  Run upload (sprue) under Delve on localhost:2345"
	@echo ""
	@echo "Options:"
	@echo "  YES=1              Skip confirmation prompts (e.g., make nuke YES=1)"
	@echo "  SMELT_WORKSPACE=1  Run containers against binaries built from your local"
	@echo "                     sibling checkouts (selected via the active go.work"
	@echo "                     use-list). 'SMELT_WORKSPACE=1 make up' compiles them"
	@echo "                     and mounts them over the published images."
	@echo "  make redeploy      Rebuild the workspace binaries, recreate their"
	@echo "                     containers and wait until they are healthy"
	@echo "                     (SMELT_WORKSPACE=1; SVC=ingot to limit)."
	@echo ""
	@echo "Destructive commands (clean, nuke, fresh) require confirmation."
	@echo ""

# Fail early if docker engine is too old. Smelt relies on features added in
# engine 25 (healthcheck.start_interval, compose top-level `name:`). On older
# engines, start_interval is silently ignored and snapshot-restored boots are
# ~3x slower than they should be.
check-docker:
	@version=$$(docker version --format '{{.Server.Version}}' 2>/dev/null); \
	major=$$(echo "$$version" | cut -d. -f1); \
	if [ -z "$$major" ]; then \
		echo "ERROR: could not determine docker engine version"; \
		echo "       is the docker daemon running?"; \
		exit 1; \
	fi; \
	if [ "$$major" -lt 25 ]; then \
		echo "ERROR: docker engine $$version is below the required minimum of 25.0"; \
		echo "       Upgrade: https://docs.docker.com/engine/install/"; \
		exit 1; \
	fi

# Seed generated/snapshot-scratch/ from the committed post-deploy baseline
# when no working chain state exists yet. After first seed, the SIGTERM dump
# from the blockchain container keeps these files current across down/up
# cycles; we never overwrite in place. `make clean` / `make nuke` clears
# scratch so the next `make up` picks up the baseline again.
ensure-state: check-docker
	@mkdir -p generated/snapshot-scratch
	@# Self-heal: if a prior compose up found a non-existent bind-mount source
	@# and docker auto-created it as a directory, remove the dir so we can
	@# seed a file in its place.
	@[ -d generated/snapshot-scratch/anvil-state.json ] && rm -rf generated/snapshot-scratch/anvil-state.json || true
	@[ -d generated/snapshot-scratch/deployed-addresses.json ] && rm -rf generated/snapshot-scratch/deployed-addresses.json || true
	@if [ ! -f generated/snapshot-scratch/anvil-state.json ] && [ -f systems/blockchain/state/anvil-state.json ]; then \
		cp systems/blockchain/state/anvil-state.json generated/snapshot-scratch/anvil-state.json; \
	fi
	@if [ ! -f generated/snapshot-scratch/deployed-addresses.json ] && [ -f systems/blockchain/state/deployed-addresses.json ]; then \
		cp systems/blockchain/state/deployed-addresses.json generated/snapshot-scratch/deployed-addresses.json; \
	fi

# Generate compose files and keys from the selected manifest. Every
# generation also records which manifest it used, so a later run can tell a
# switch to another manifest apart from an unchanged selection.
MANIFEST_STAMP := generated/compose/.manifest-path
GENERATE := go run ./cmd/smelt generate && mkdir -p $(dir $(MANIFEST_STAMP)) && echo "$(MANIFEST)" > $(MANIFEST_STAMP)
generate:
	@$(GENERATE)

# File target: rebuild the generated piri compose when the manifest or
# generator source changes. Compose-invoking targets below depend on this
# so fresh checkouts and post-nuke states regenerate piri.yml on demand.
generated/compose/piri.yml: $(MANIFEST) $(shell find cmd/smelt pkg/generate pkg/manifest -name '*.go' 2>/dev/null) | manifest-switch
	@$(GENERATE)

# Timestamps miss a switch between two existing manifests (both are older
# than piri.yml), so this order-only prerequisite regenerates whenever the
# selected manifest differs from the one recorded by the last generation.
manifest-switch:
	@if [ -f generated/compose/piri.yml ] && [ "$$(cat $(MANIFEST_STAMP) 2>/dev/null)" != "$(MANIFEST)" ]; then $(GENERATE); fi
.PHONY: manifest-switch

# Initialize the environment (generate keys, proofs, create network)
init: generate
	@./scripts/init.sh

# Start all services (runs init first if needed) and wait until every service
# with a health check reports healthy.
#
# Pass SNAPSHOT=<name-or-path> to load a snapshot before starting — keys,
# proofs, blockchain state, docker volumes, and a session manifest at
# generated/snapshot-scratch/smelt.yml are all populated from it. The
# project's tracked smelt.yml is never touched; subsequent `make up` calls
# (with or without SNAPSHOT) stay on the session manifest until `make clean`
# or `make nuke` removes it.
up: ensure-state
	@if [ -n "$(SNAPSHOT)" ]; then \
		echo "Loading snapshot: $(SNAPSHOT)"; \
		go run ./cmd/smelt snapshot load "$(SNAPSHOT)"; \
	fi
	@if [ ! -d "generated/keys" ] || [ -z "$$(ls -A generated/keys 2>/dev/null)" ]; then \
		$(MAKE) init; \
	else \
		$(MAKE) generate; \
		if command -v "$${UCANTOOL:-ucantool}" >/dev/null 2>&1; then \
			./generated/generate-proofs.sh; \
		else \
			echo "WARNING: ucantool not found - skipping proof top-up"; \
		fi \
	fi
	$(MAKE) workspace-build
	$(COMPOSE) up -d --remove-orphans
	@echo ""
	@echo "Services starting. Run 'make status' to check health."
	@echo "Run 'make logs' to follow logs."
	@echo ""
	@echo "Waiting for services to become healthy..."
	@./scripts/wait-healthy.sh

# Stop all services (keeps volumes for quick restart)
down: generated/compose/piri.yml ensure-state
	$(COMPOSE) down --remove-orphans
	@echo ""
	@echo "Services stopped. Data preserved in volumes."
	@echo "Run 'make up' to restart."

# Restart all services
restart: down up

# Helper to confirm destructive operations
define confirm
	@if [ "$(YES)" != "1" ]; then \
		echo ""; \
		echo "WARNING: This will $(1)"; \
		echo ""; \
		read -p "Are you sure? [y/N] " confirm && [ "$$confirm" = "y" ] || (echo "Aborted." && exit 1); \
	fi
endef

# Stop services and remove volumes (but keep keys/proofs)
clean: generated/compose/piri.yml check-docker
	$(call confirm,STOP all services and DELETE all volumes (Redis cache$(,) IPNI data$(,) etc.))
	@# Stop all services including those with profiles
	$(COMPOSE) down -v --remove-orphans
	@# Also remove any dangling volumes from this project
	$(DOCKER) volume ls -q --filter "name=smelt_" | xargs -r $(DOCKER) volume rm 2>/dev/null || true
	@# Clear chain state so next `make up` cold-boots from the committed baseline.
	@# Leaving it would produce a half-warm stack: empty volumes but a mutated chain.
	rm -rf generated/snapshot-scratch/anvil-state.json generated/snapshot-scratch/deployed-addresses.json
	@# End any active snapshot session so `make up` goes back to project smelt.yml.
	rm -f generated/snapshot-scratch/smelt.yml
	@# Drop any workspace binary-mount override so the next plain `make up`
	@# starts from published images.
	rm -f $(WORKSPACE_OVERRIDE)
	@echo ""
	@echo "Services stopped, volumes removed, chain state reset."
	@echo "Keys and proofs preserved. Run 'make up' to restart."

# Remove EVERYTHING - volumes, keys, proofs, and built images
nuke: generated/compose/piri.yml check-docker
	$(call confirm,DELETE everything: containers$(,) volumes$(,) keys$(,) proofs$(,) AND Docker images)
	@echo "Removing all containers, volumes, keys, proofs, and images..."
	@# Stop all services including those with profiles
	$(COMPOSE) down -v --remove-orphans --rmi local 2>/dev/null || true
	@# Also remove any dangling volumes from this project
	$(DOCKER) volume ls -q --filter "name=smelt_" | xargs -r $(DOCKER) volume rm 2>/dev/null || true
	rm -rf generated/keys generated/proofs generated/compose
	rm -rf generated/snapshot-scratch/anvil-state.json generated/snapshot-scratch/deployed-addresses.json
	rm -f generated/snapshot-scratch/smelt.yml
	@echo ""
	@echo "Everything removed. Run 'make up' or 'make fresh' to start over."

# Complete fresh start - nuke everything, rebuild, and start
fresh: generated/compose/piri.yml check-docker
	$(call confirm,DELETE everything and rebuild from scratch)
	@echo "Removing all containers, volumes, keys, proofs, and images..."
	@# Stop all services including those with profiles
	$(COMPOSE) down -v --remove-orphans --rmi local 2>/dev/null || true
	@# Also remove any dangling volumes from this project
	$(DOCKER) volume ls -q --filter "name=smelt_" | xargs -r $(DOCKER) volume rm 2>/dev/null || true
	rm -rf generated/keys generated/proofs generated/compose
	rm -rf generated/snapshot-scratch/anvil-state.json generated/snapshot-scratch/deployed-addresses.json
	rm -f generated/snapshot-scratch/smelt.yml
	@echo ""
	@echo "Rebuilding and starting fresh..."
	$(MAKE) init
	$(MAKE) ensure-state
	$(MAKE) workspace-build
	$(COMPOSE) build
	$(COMPOSE) up -d --remove-orphans
	@echo ""
	@echo "Waiting for services to become healthy..."
	@./scripts/wait-healthy.sh
	@echo ""
	@echo "Fresh deployment complete!"
	@echo ""
	@echo "Next steps:"
	@echo "  make shell-guppy       Open guppy shell"
	@echo "  guppy login EMAIL      Create account"
	@echo "  guppy space generate   Create a space"

# Regenerate keys and proofs (requires service restart to take effect)
regen:
	@echo "Regenerating keys and proofs..."
	go run ./cmd/smelt generate --force
	./generated/generate-proofs.sh --force
	@echo ""
	@echo "Keys and proofs regenerated."
	@echo "Run 'make clean && make up' to restart services with new keys."

# Pull latest pre-built images (ignores failures for local-only images)
pull: generated/compose/piri.yml ensure-state
	$(COMPOSE) pull --ignore-pull-failures

# Build the smelt CLI binary to ./smelt (used by the snapshot commands and
# anywhere the docs reference `./smelt ...`). Rebuilt whenever any Go source
# under cmd/smelt or pkg changes.
smelt: $(shell find cmd/smelt pkg -name '*.go' 2>/dev/null)
	go build -o smelt ./cmd/smelt

# Convenience alias for building the CLI binary.
cli: smelt

# Build all images
build: generated/compose/piri.yml ensure-state
	$(MAKE) workspace-build
	$(COMPOSE) build

# Follow logs from all services
logs: generated/compose/piri.yml ensure-state
	$(COMPOSE) logs -f

# Show service status
status: generated/compose/piri.yml ensure-state
	@$(COMPOSE) ps
	@echo ""
	@$(COMPOSE) ps --format "table {{.Name}}\t{{.Status}}" | grep -E "(healthy|unhealthy|starting)" || true

# Mint an S3 access key via hilt and save it as an AWS CLI profile pointed at
# ingot. TENANT and PROFILE default to "dev" and "smelt"; see scripts/s3-key.sh.
s3-key: generated/compose/piri.yml ensure-state
	@TENANT="$(TENANT)" PROFILE="$(PROFILE)" ./scripts/s3-key.sh

# Shell into guppy container
shell-guppy: generated/compose/piri.yml ensure-state
	$(COMPOSE) exec guppy bash

# Shell into piri-0 container
shell-piri: generated/compose/piri.yml ensure-state
	$(COMPOSE) exec piri-0 sh

# Shell into upload container
shell-upload: ensure-state
	$(COMPOSE) exec upload bash

# Shell into hilt container
shell-hilt: ensure-state
	$(COMPOSE) exec hilt bash

# Rebuild the workspace binaries and recreate the containers that run them,
# leaving the rest of the stack (chain state, init services, volumes) alone,
# then wait until the recreated containers report healthy. SVC=ingot
# (comma-separated list allowed) limits both the build and the recreate to
# those services. Containers are recreated rather than restarted:
# the binary is a file bind mount resolved when the container is created, and
# the build installs a new file (new inode) under the same path. Only the
# current go.work selection is recreated; after dropping a module from the
# use-list, `make up` is what recreates its container without the mount.
redeploy: generated/compose/piri.yml ensure-state
	@if [ "$(SMELT_WORKSPACE)" != "1" ]; then \
		echo "ERROR: redeploy needs SMELT_WORKSPACE=1 (binaries come from the go.work checkouts)"; \
		exit 1; \
	fi
	go run ./cmd/smelt workspace build $(if $(SVC),--only $(SVC))
	@# Resolve the container list first and refuse to continue when it is
	@# empty: `up --force-recreate` with no service args would recreate the
	@# whole stack.
	@services=$$(go run ./cmd/smelt workspace services $(if $(SVC),--only $(SVC))) || exit 1; \
	if [ -z "$$services" ]; then echo "ERROR: no workspace services to redeploy"; exit 1; fi; \
	echo "Recreating: $$services"; \
	$(COMPOSE) up -d --no-deps --force-recreate $$services && \
	echo "Waiting for services to become healthy..." && \
	./scripts/wait-healthy.sh $$services

# Run upload (sprue) under Delve for remote debugging.
# See compose.debug.yml for the overlay; attach to localhost:2345.
debug-upload: generated/compose/piri.yml ensure-state
	@if [ ! -d "generated/keys" ] || [ -z "$$(ls -A generated/keys 2>/dev/null)" ]; then \
		$(MAKE) init; \
	fi
	$(DOCKER) compose -f compose.yml -f compose.debug.yml up -d --force-recreate upload upload-init
	@echo ""
	@echo "upload is running under Delve. Attach to localhost:2345:"
	@echo "  dlv connect localhost:2345"
	@echo "  (or VS Code 'Connect to server' / GoLand 'Go Remote')"
