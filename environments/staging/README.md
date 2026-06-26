# Forge staging deployment

Version-pinned Docker Compose manifests and configuration for the Forge **staging**
environment on the Servers.com Calibnet box (`root@23.83.66.244`). The stack is split
into two independently-deployed bundles — [`core/`](core/) (sprue + signing-service +
delegator and their dependency containers) and [`piri/`](piri/) (a piri storage node) —
which talk to each other over public `https://*.staging.fil.one` URLs fronted by the
host's Caddy reverse proxy. Image versions are pinned in each bundle's `versions.env`;
non-secret config lives in committed `config.env` / templates, and secret values come
from 1Password at provision time (never committed). Delegation proofs in [`proofs/`](proofs/)
are generated once by `smelt staging keygen` and committed.

See **[docs/STAGING_DEPLOY.md](../../docs/STAGING_DEPLOY.md)** for the full fresh-host
bootstrap and deploy runbook.
