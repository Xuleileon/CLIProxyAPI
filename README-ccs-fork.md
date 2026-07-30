# CCS Fork Notes

This is the CCS-maintained fork of `router-for-me/CLIProxyAPIPlus`, snapshotted at commit `0c48ef58` (2026-04-17) under MIT before the upstream repo was deleted and rebranded as the SSPL-licensed `CLIProxyAPIBusiness`.

## Why this fork exists

- Upstream `router-for-me/CLIProxyAPIPlus` was deleted 2026-04-17.
- Successor repo `CLIProxyAPIBusiness` is SSPL-licensed — incompatible with CCS redistribution.
- Plus providers (`codebuddy`, `copilot`, `cursor`, `gitlab`, `iflow`, `kilo`, `kiro`) do **not** exist in the still-alive `router-for-me/CLIProxyAPI` (original MIT). They live only here.
- Our MIT snapshot predates the rebrand; MIT rights do not retroactively revoke.

## Upstream Sync

Daily GitHub Action (`.github/workflows/upstream-sync.yml`) merges from `router-for-me/CLIProxyAPI` (original, still MIT, still public). Plus-only provider directories are guarded by `.gitattributes merge=ours` — upstream can never touch them.

- **Clean merge + gates green** → auto fast-forward to `main`.
- **Conflicts OR build/test fails** → opens PR labeled `upstream-sync`.
- Manual trigger: Actions → "Upstream Sync" → Run workflow.
- Selective batch plan and status: [docs/upstream-sync-backlog.md](docs/upstream-sync-backlog.md).

## What NOT to pull in

- Any code from `router-for-me/CLIProxyAPIBusiness` — SSPL would infect this fork and any downstream user of CCS. Do not copy, cherry-pick, or look at it as reference.
- Fixes to the 7 plus-only providers must be self-authored or sourced from MIT-compatible contributions only.

## Releases

`release.yaml` builds via goreleaser on tag push. Binary name `cli-proxy-api-plus`, archives `CLIProxyAPIPlus_<ver>_<os>_<arch>.*`. CCS CLI's `BACKEND_CONFIG.plus.repo` points here, so tagging a release (e.g., `v6.9.19-ccs.1`) publishes binaries CCS users pick up automatically.

## Related issues

- CCS CLI #1062 — runtime degrades `backend: plus → original` until this fork's releases are wired up in `BACKEND_CONFIG.plus.repo`.
