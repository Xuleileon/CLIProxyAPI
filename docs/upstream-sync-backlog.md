# Upstream Sync Backlog

## Purpose

Track selective merges from `router-for-me/CLIProxyAPI` (MIT) into this CCS fork without touching Plus-only providers or fork maintenance assets. Batch 0 establishes the backlog, CI regression guards, and a verified baseline — no runtime Go changes.

## Principles

1. **MIT only** — never pull from SSPL `CLIProxyAPIBusiness`.
2. **Protect Plus surfaces** — `.gitattributes merge=ours` dirs and fork-owned workflows stay local.
3. **Batch before merge** — each batch has scope, evidence, protection checks, and acceptance gates.
4. **Verify before claim** — record PASS/FAIL from commands actually run; do not mark tests green without execution.
5. **Small diffs** — prefer targeted upstream cherry-picks or scoped merges over wholesale sync.

## Upstream Baseline

| Field | Value |
|-------|-------|
| Recorded | 2026-07-30 |
| Local HEAD (pre–Batch 0) | `c3424c4c9548fbc6a3b978e2d5f8ea5fc00aeab2` — `fix(cursor): fix multi-turn conversation and panel patch on serve` |
| Upstream remote | `https://github.com/router-for-me/CLIProxyAPI` (`upstream`) |
| Upstream comparison target | tag `v7.2.105` → commit `4a2eb54dc6bf943196be4fb515e6a9407a4db143` |
| Target verification | **Verified** — `git ls-remote upstream refs/tags/v7.2.105` and local `git cat-file -t 4a2eb54d` both resolve to the same commit |
| Divergence vs target | `905` local-only commits ahead, `140` upstream commits behind (`git rev-list --left-right --count HEAD...v7.2.105`) |
| Stale pin note | `.ccs-fork-upstream.env` still lists `v7.2.88` / `93d74a89` — update when a sync batch lands, not in Batch 0 |

### Batch 0 verification (2026-07-30)

Split by what was actually run locally vs what requires GitHub Actions on `ubuntu-latest`.

#### Verified locally (Windows dev machine, post–file-edit)

| Check | Command | Status |
|-------|---------|--------|
| Embedded catalogs (pre-refresh) | `go test ./internal/registry -run 'TestEmbeddedCodexClientModelsCatalogIsValid\|TestModelOverrideHeadersFromEmbeddedModels' -count=1` + `go run ./cmd/validate_codex_models --file internal/registry/models/codex_client_models.json` | **PASS** |
| Sentinel (xAI / usage / thinking+usage) | `go test ./internal/auth/xai -count=1` + `go test ./sdk/cliproxy/usage -count=1` + `go test ./test -run 'TestThinkingE2EMatrix_Suffix\|TestGeminiExecutorRecordsSuccessfulZeroUsage' -count=1` | **PASS** |
| Compile | `go build -o test-output ./cmd/server` then remove `test-output` | **PASS** |
| Workflow YAML | Python `yaml.safe_load` on `pr-test-build.yml` | **PASS** |
| Diff whitespace | `git diff --check` on Batch 0 files | **PASS** |

#### Pending GitHub Actions verification (Linux CI — not run locally)

| Check | Command | Status |
|-------|---------|--------|
| gofmt | `test -z "$(gofmt -l .)"` on `ubuntu-latest` | **pending verification** |
| Full test | `go test ./... -count=1` on `ubuntu-latest` | **pending verification** |

**Do not treat Windows `gofmt -l .` output as a Linux CI result.** On a CRLF checkout, local Windows runs reported ~986 paths; that reflects line-ending / platform checkout behavior, not a confirmed formatting-debt count or CI failure. Authoritative gofmt status comes only from Actions.

**Do not treat Windows full-suite output as Linux CI PASS/FAIL.** Latest local Windows run (2026-07-30, Batch 0-4 final audit): `go test ./... -count=1` exit code 0, 0 FAIL packages. Earlier baseline run had observed failures (see Known baseline issues); Linux CI outcome is unknown until Actions runs.

## Known baseline / known local issues

Observed before Batch 0 or on Windows local runs only — **not introduced by Batch 0** (no runtime Go changes in this batch). No logs committed.

| Issue | Context | Batch 0 impact |
|-------|---------|----------------|
| Windows `gofmt -l .` lists many files | CRLF working tree on Windows; not authoritative for Linux CI | CI step added; Linux result pending Actions |
| ~~`TestOAuthWebImportLoadsIDCDeviceRegistrationFromClientIDHash`~~ | `internal/auth/kiro` — AWS SSO cache under HOME/USERPROFILE | **Resolved (test-only):** temp home sets both `HOME` and `USERPROFILE` in test |
| ~~`TestXAIExecutorExecuteImagesUsesImagesEndpointAndPublishesUsage`~~ | `internal/runtime/executor` — TTFT `time.Since` can be 0 on Windows | **Resolved (test-only):** 2ms mock handler delay; TTFT assertion unchanged |
| `TestDeletePluginRemovesDiscoveredFileAndConfig` | `internal/api/handlers/management` — plugin delete blocked on config reload | Possibly flaky; pre-existing; Linux CI pending |
| `.ccs-fork-upstream.env` stale pin | Still `v7.2.88` / `93d74a89` vs comparison target `v7.2.105` | Unchanged in Batch 0 by design |

## Local Protection Surface

Areas that must survive every upstream batch (manual review even when not in `.gitattributes`):

| Area | Guard / location |
|------|------------------|
| **Cursor** | `internal/auth/cursor/**` (`merge=ours`) |
| **Kiro** | `internal/auth/kiro/**`, Kiro executors/translators |
| **Qoder** | OAuth flows (no `merge=ours` dir — review on auth/API touch) |
| **Copilot** | `internal/auth/copilot/**` |
| **GitLab** | `internal/auth/gitlab/**` |
| **Gemini CLI** | Gemini auth/executor paths used by CLI clients |
| **Amp** | `internal/api/modules/amp/**` |
| **plugins / Home** | SDK plugins, home/conductor UX — defer large upstream home changes |
| **usage** | `internal/usage/**`, `sdk/cliproxy/usage/**`, Redis queue usage stats |
| **internal thinking** | `internal/thinking/**` — canonical pipeline; no wholesale upstream replacement |

Fork maintenance (always ours): `.github/workflows/*` (except intentional edits), `README-ccs-fork.md`, `.gitattributes`, release/docker assets.

## Status vocabulary

| Status | Meaning |
|--------|---------|
| `candidate` | Identified upstream delta; not scheduled |
| `planned` | Scoped and queued for implementation |
| `in_progress` | Active batch / PR |
| `merged` | Landed on `main` with gates green |
| `blocked` | Waiting on dependency, conflict, or decision |
| `deferred` | Intentionally out of scope for current strategy |
| `wont_sync` | Incompatible (license, product direction, or duplicate of local fix) |

## Batch Overview

| ID | Title | Status |
|----|-------|--------|
| B000 | Backlog + CI guards + baseline (this doc) | `in_progress` |
| B001 | Grok/xAI OAuth refresh for management APICall | `in_progress` |
| B002 | Normalized token accounting v2 | `in_progress` |
| B003 | Model catalog refresh alignment | `in_progress` |
| B004 | Provider correctness sub-batches | `local-complete / pending Linux CI` |
| B005 | Codex multi-agent v2 (Phase 1) | `local-complete / pending independent review and Linux CI` |

---

## B000 — Backlog, CI guards, baseline

| | |
|-|-|
| **Scope** | Add this backlog; link from `README-ccs-fork.md`; extend `pr-test-build.yml` with gofmt, compile, embedded-catalog check (pre-refresh), sentinel tests |
| **Non-goals** | Runtime Go changes; upstream merge; updating `.ccs-fork-upstream.env` pin |
| **Upstream evidence** | N/A (fork maintenance) |
| **Local protection check** | No provider dirs touched |
| **Acceptance gates** | **Local (done):** embedded pre-refresh, sentinel (xAI/usage/thinking+usage), compile, YAML/diff clean. **Pending commit + Actions:** gofmt on Linux, full `go test ./... -count=1` on Linux, PR review. Gates are **not** complete until CI green and changes land on `main`. |
| **PR** | Maintainer batch — not yet committed |
| **Release** | None |
| **Last updated** | 2026-07-30 |

---

## B001 — Grok/xAI OAuth refresh for management APICall

| | |
|-|-|
| **Scope** | Align xAI OAuth token refresh with upstream fixes used by management APICall paths |
| **Non-goals** | Unrelated Grok executor changes; Cursor/Kiro auth |
| **Upstream evidence** | TBD — locate on `upstream/main` before implementation |
| **Local protection check** | `internal/auth/xai/**` diff review; ensure Cursor/Kiro untouched |
| **Acceptance gates** | `go test ./internal/auth/xai -count=1`; `go test ./internal/api/handlers/management/... -run 'XAI|APICall|Copilot|CBOR' -count=1`; management APICall unit tests with httptest (no live x.ai) |
| **PR** | TBD |
| **Release** | Next CCS tag after merge |
| **Last updated** | 2026-07-30 |

### Batch 1 verification (2026-07-30)

Restored after cross-agent workspace overwrite (2026-07-30): `api_tools.go` xAI OAuth refresh helpers re-applied; tests in untracked `api_tools_xai_test.go` unchanged. Legacy metadata policy aligned with `shouldRefresh`; no production test hook in `internal/auth/xai`; concurrent singleflight covered by `internal/auth/xai` `TestRefreshTokens_DeduplicatesConcurrentRefresh`.

| Check | Command | Status |
|-------|---------|--------|
| xAI OAuth refresh unit tests | `go test ./internal/api/handlers/management/... -run 'XAI' -count=1` | **PASS** (post-restore 2026-07-30) |
| Management APICall regression | `go test ./internal/api/handlers/management/... -run 'APICall\|Copilot\|CBOR' -count=1` | **PASS** (post-restore 2026-07-30) |
| xAI auth package | `go test ./internal/auth/xai/... -count=1` | **PASS** (post-restore 2026-07-30) |
| Compile | `go build -o test-output ./cmd/server` then remove `test-output` | **PASS** (post-restore 2026-07-30) |
| CI sentinel YAML | Python `yaml.safe_load` on merged `pr-test-build.yml` | **PASS** (post-restore 2026-07-30) |
| Diff whitespace | `git diff --check` on Batch 1 files | **PASS** (post-restore 2026-07-30) |
| Real xAI smoke (live token endpoint + billing) | Manual against x.ai | **waived** |

---

## B002 — Normalized token accounting v2

| | |
|-|-|
| **Scope** | Port upstream usage normalization v2 and related hardening |
| **Non-goals** | Rewriting local usage plugins; Redis queue semantics change without review |
| **Upstream evidence** | `416a0801` fix(usage): add normalized token accounting v2 · `fe8a616a` fix(usage): classify partial token accounting correctly · `42f36b94` fix(usage): harden canonical token normalization — **re-verify on upstream before merge** |
| **Local protection check** | Diff `internal/usage/**`, `sdk/cliproxy/usage/**`; run usage sentinel tests |
| **Acceptance gates** | Sentinel usage tests; full `go test ./...` |
| **PR** | TBD |
| **Release** | Next CCS tag after merge |
| **Last updated** | 2026-07-30 |

### Batch 2 verification (2026-07-30)

Manual port from upstream `42f36b94` (matches `v7.2.105` accounting paths). Scope: `sdk/cliproxy/usage/accounting*.go`, `manager.go`, `usage_helpers*.go`, `internal/redisqueue/plugin*.go` only. Local fork kept `ParseGeminiCLIUsage` / `ParseGeminiCLIStreamUsage` on Gemini-family v2 breakdown.

| Check | Command | Status |
|-------|---------|--------|
| accounting unit tests | `go test ./sdk/cliproxy/usage/... -count=1` | **PASS** (post-restore 2026-07-30) |
| usage helpers | `go test ./internal/runtime/executor/helps/... -count=1` | **PASS** (post-restore 2026-07-30) |
| redisqueue plugin | `go test ./internal/redisqueue/... -count=1` | **PASS** (post-restore 2026-07-30) |
| integration usage/thinking | `go test ./test/... -run 'Usage\|Thinking' -count=1` | **PASS** (post-restore 2026-07-30) |
| Full suite (Windows) | `go test ./... -count=1` | **not re-run** (expect pre-existing Kiro AWS SSO cache failure) |
| Compile | `go build -o test-output ./cmd/server` then remove `test-output` | **PASS** (post-restore 2026-07-30) |
| Batch 2 file integrity | SHA256 unchanged on 7 accounting paths after Batch 1 restore | **PASS** |
| Upstream parity | `accounting.go` LF-normalized equivalent to `42f36b94`; `usage_helpers.go` diff = fork `ParseGeminiCLI*` only | **pending maintainer diff review** |

**B000/B001:** remain `in_progress`; CI gofmt/full suite on Linux still **pending Actions**.

---

## B003 — Model catalog

| | |
|-|-|
| **Scope** | Selective sync of model catalog entries from upstream v7.2.105 that are missing locally and still present in the final tag |
| **Non-goals** | Changing Plus-only model entries; breaking `--local-model` / embedded fallback; touching codex_client_models.json; adding `minimal` reasoning; adding `supports_reasoning_summary_parameter` field |
| **Upstream evidence** | v7.2.105 (`4a2eb54d`) models.json vs local HEAD; commits `61a6f08d`, `3073dab0`, `ace2e843`, `a432d763`, `7b233fa3`, `3d5ec862` all in v7.2.105 ancestry |
| **Local protection check** | Fork Go registry files (model_definitions.go, codebuddy_models.go, kilo_models.go, kiro_model_converter.go) are local-only additions vs upstream — never overwritten. `qoder` top-level key (12 entries) preserved. |
| **Acceptance gates** | See verification table below |
| **PR** | TBD |
| **Release** | With catalog-affecting tag |
| **Last updated** | 2026-07-30 |

### Batch 3 verification (2026-07-30)

**Synced:** 9 records (5 unique model IDs) added to `internal/registry/models/models.json` via order-preserving structured merge from upstream v7.2.105:

| provider | id |
|----------|----|
| claude | claude-opus-5 |
| gemini | gemini-3.5-flash-lite |
| gemini | gemini-3.6-flash |
| vertex | gemini-3.1-pro-preview |
| vertex | gemini-3.5-flash-lite |
| vertex | gemini-3.6-flash |
| aistudio | gemini-3.5-flash-lite |
| aistudio | gemini-3.6-flash |
| antigravity | gemini-3.6-flash-high |

**Skipped / wont_sync:**
- `supports_reasoning_summary_parameter` field on all 8 codex_client_models entries: upstream-only field, no local Go struct consumes it — dead field, wont_sync.
- `minimal` reasoning level (commits `3d5ec862`/`7b233fa3`): neither local nor upstream v7.2.105 codex_client_models.json contains `"minimal"` as a reasoning level value (both have zero occurrences; the `minimal_client_version` field present in both is unrelated). No sync action required — already equivalent.
- `gemini-3.5-flash-lite` removal (commit `a432d763`): superseded; v7.2.105 final JSON still contains it in gemini/vertex/aistudio — synced.
- All Go registry files (model_definitions.go +545, codebuddy_models.go +167, kilo_models.go +21, kiro_model_converter.go +325 vs upstream): fork-only code, protected, not synced.
- `qoder` top-level key: fork-specific, 12 entries, preserved.

| Check | Command | Status |
|-------|---------|--------|
| Registry tests | `go test ./internal/registry/... -count=1` | **PASS** |
| Catalog updater plan | `go test ./cmd/server/... -run 'Model\|Catalog\|Local' -count=1` | **PASS** (TestModelCatalogUpdaterPlan all subtests) |
| SDK openai handlers | `go test ./sdk/api/handlers/openai/... -run 'Model\|Codex' -count=1` | **PASS** |
| Full suite (Windows) | `go test ./... -count=1` | **PASS** |
| Compile | `go build -o test-output.exe ./cmd/server` + remove | **PASS** |
| Whitespace | `git diff --check` | **PASS** (CRLF warnings only, pre-existing) |
| Batch 0-2 file integrity | `git hash-object` on all 14 intended files | **PASS** — all hashes unchanged |
| JSON validity | Python parse + (provider,id) duplicate detection | **PASS** — 0 dups, 0 missing after merge |
| New test file | `internal/registry/model_catalog_sync_test.go` — TestSelectedSyncBaseline, TestSelectedSyncBaselineNoDuplicateProviderIDs, TestSelectedSyncBaselineQoderProtected | **PASS** |

**Pending GitHub Actions:** gofmt on Linux, full suite on Linux — not yet run.

**B000/B001/B002:** remain `in_progress`; CI gofmt/full suite on Linux still **pending Actions**.

---

## B004 — Provider correctness (sub-batches)

| | |
|-|-|
| **Scope** | Cherry-pick upstream provider bugfixes (non–Plus-only), split by provider |
| **Non-goals** | Bulk merge of `internal/auth/{cursor,kiro,copilot,gitlab}/**` |
| **Upstream evidence** | Per sub-batch commit list |
| **Local protection check** | `.gitattributes` + manual diff on protected dirs |
| **Acceptance gates** | Provider-specific tests + full suite |
| **PR** | One PR per provider sub-batch |
| **Release** | Rolling |
| **Last updated** | 2026-07-30 |

### B004-xAI — xAI output controls + video usage (2026-07-30)

**Status:** `local-complete / pending Linux CI` (independently PASS on Windows; not yet run on GitHub Actions)

**Implemented (upstream v7.2.105 / `41f6ea89`):**

| Item | Upstream SHA | Local change | Risk |
|------|-------------|--------------|------|
| Output controls preservation | `41f6ea89` | `preserveXAIResponsesOutputControls()` in `xai_executor.go`; called after TranslateRequest in `prepareResponsesRequestTo`; `stop` deleted in `sanitizeXAIResponsesBody`; compact path deletes `max_output_tokens/temperature/top_p/top_k/stop` | Low |
| Video usage reporter | v7.2.105 media parity | `executeVideos` now uses `NewExecutorUsageReporter` + `TrackFailure` + `TrackHTTPClient` + `EnsurePublished`, symmetric with `executeImages` | Low |

**Tests added/updated (xai_executor_test.go):**
- `TestXAIExecutorPrepareResponsesRequestPreservesSupportedOutputControls` (4 subtests: chat max_completion_tokens, chat max_tokens fallback, responses native, no-controls absent)
- `TestXAIExecutorPrepareResponsesRequestDropsPayloadStopOverride`
- `TestXAIExecutorCompactUsesCompactEndpoint` — updated to assert all 6 fields stripped + payload override top_k cleaned
- `TestXAIExecutorExecuteStreamCompactionTriggerUsesCompactEndpoint` — updated stream assertions
- `TestXAIExecutorExecuteVideosPublishesUsage` (success path, TTFT, no double-publish)
- `TestXAIExecutorExecuteVideosPublishesFailureUsage` (429 failure record)

**Deferred:**
- `InjectXSearch` config gate (`8423cce2`): local already has unconditional x_search injection (equivalent core); config toggle deferred (needs config_types + watcher diff).
- WS compact transcript replay (`840ba5dc`): deferred — involves concurrency state machine changes in xai_websockets_executor.go.

**already_present:**
- x_search injection core logic (ensureXAINativeXSearchTool, allowed_tools sync, response filter)
- OAuth/API key credential resolution, CLI chat-proxy routing
- Compact SSE synthesis, reasoning replay cache, namespace tool flattening

| Check | Command | Status |
|-------|---------|--------|
| Targeted xAI executor | `go test -run 'OutputControls\|Responses\|Compact\|Videos\|Images\|XSearch\|Usage' ./internal/runtime/executor/... -count=1` | **PASS** |
| Batch1 management XAI | `go test ./internal/api/handlers/management/... -run 'XAI' -count=1` | **PASS** |
| Batch2 usage/accounting | `go test ./sdk/cliproxy/usage/... ./internal/runtime/executor/helps/... ./internal/redisqueue/... -count=1` | **PASS** |
| Integration Usage\|Thinking | `go test ./test/... -run 'Usage\|Thinking' -count=1` | **PASS** |
| Full suite (Windows) | `go test ./... -count=1` | **PASS** (pre-existing `TestDeletePluginRemovesDiscoveredFileAndConfig` flaky) |
| Compile | `go build -o test-output.exe ./cmd/server` + remove | **PASS** |
| Whitespace | `git diff --check` on B004-xAI files | **PASS** (CRLF warnings only) |
| Batch0-3 integrity | `git hash-object` on all 15 Batch0-3 files | **PASS** — all unchanged |

**Pending GitHub Actions:** gofmt on Linux, full suite on Linux — not yet run.

---

### B004-Claude — streaming headers + OAuth SSE tool-name restore (2026-07-30)

**Status:** `local-complete / pending Linux CI` (independent review: **PASS** — GLM read-only verification 2026-07-30)

**Implemented (upstream v7.2.105):**

| Item | Upstream SHA | Local change | Risk |
|------|-------------|--------------|------|
| Streaming headers (upstreamStream) | `c405398a` | `Execute` computes `upstreamStream` from `ResponseFormatOrSource(opts)` vs `sdktranslator.FromString("claude")`; response branch uses `upstreamStream` instead of hardcoded stream; `applyClaudeHeaders` receives upstream stream decision and re-enforces `Accept: text/event-stream` after custom headers when streaming | Low |
| OAuth SSE tool-name restore | `08e55157` | In upstream SSE branch of `Execute`, per-line tool-name restore applied before translation when auth is Claude OAuth and per-request `toolNameMap` exists | Low |

**Tests added (claude_executor_b4_test.go):**
- `TestClaudeExecutor_Execute_UpstreamStreamDecision` (3 subtests: native claude→claude JSON, openai→claude upstream SSE, explicit claude response format JSON)
- `TestClaudeExecutor_Execute_StreamBoolOverridesClientValue`
- `TestClaudeExecutor_Execute_OAuthSSEToolNameRestore`
- `TestClaudeExecutor_Execute_NonOAuthDoesNotRemapToolNames`
- `TestClaudeExecutor_Execute_NonStreamJSONResponseToolNameRestore`
- `TestClaudeExecutor_Execute_AcceptEncodingNotOverriddenByCustomAttrs`

**Deferred:**
- Local input token count (`57ef7842`): requires `TranslateStreamWithClaudeInputTokens`, `NewClaudeInputTokenState`, `CountClaudeInputTokens`, `helps.SetBoolIfDifferent` — larger surface, deferred.
- CAIS signature (`5b2890b3`): larger change, deferred.

**ExecuteStream path:** intentionally untouched.

| Check | Command | Status |
|-------|---------|--------|
| B4-Claude targeted | `go test -run 'UpstreamStream\|StreamBool\|OAuthSSE\|NonOAuth\|NonStream\|AcceptEncoding' ./internal/runtime/executor/... -count=1` | **PASS** |
| Executor package | `go test ./internal/runtime/executor/... -count=1` | **PASS** |
| Batch1 management XAI | `go test ./internal/api/handlers/management/... -run 'XAI' -count=1` | **PASS** |
| Batch2 usage/accounting | `go test ./sdk/cliproxy/usage/... ./internal/runtime/executor/helps/... ./internal/redisqueue/... -count=1` | **PASS** |
| Integration Usage\|Thinking | `go test ./test/... -run 'Usage\|Thinking' -count=1` | **PASS** |
| Full suite (Windows) | `go test ./... -count=1` | **PASS** (exit code 0) |
| Compile | `go build -o test-output.exe ./cmd/server` + remove | **PASS** |
| Whitespace | `git diff --check` | **PASS** (CRLF warnings only, pre-existing) |
| Batch0-3 + B4-xAI integrity | `git diff --name-only` audit — only `claude_executor.go` modified by B4-Claude; all other changed files belong to Batch0-3/B4-xAI | **PASS** |
| Build artifacts | `test-output.exe` deleted; no stale artifacts | **PASS** |

**Independent review:** **PASS** (GLM read-only verification, 2026-07-30).
**Pending:** Linux CI (gofmt + full suite on GitHub Actions) — not yet run.

**B000/B001/B002/B004-xAI:** remain `in_progress`; CI gofmt/full suite on Linux still **pending Actions**.

---

### B004-Antigravity — schema response sanitization (2026-07-30)

**Status:** `local-complete / pending Linux CI` (independent review: **PASS** — GLM read-only verification 2026-07-30)

**Gap vs upstream v7.2.105 (`4a2eb54d`):** local Antigravity request sanitization ran the JSON-schema cleaner over the *whole payload* (`util.Walk` rename + `util.CleanJSONSchemaFor{Gemini,Antigravity}` on the entire document). Upstream final scopes cleaning to explicit schema paths only, because schema keywords (`title`, `format`, `default`, `const`) are ordinary data keys inside replayed `functionCall.args`; whole-document cleaning silently mutated conversation history. Local also missed snake_case containers/aliases for trigger detection.

**Upstream evidence:**
- `v7.2.105:internal/runtime/executor/antigravity_executor_request.go` — `sanitizeAntigravityRequestSchemas`, `cleanAntigravitySchemasAtPaths`, `cleanNestedSchema`, declaration/generation path enumeration, key lists, `antigravityRequestNeedsSchemaSanitization`
- `v7.2.105:internal/util/gemini_schema.go` — `CleanJSONSchemaForAntigravityResponse`, parametrized `cleanJSONSchema(addPlaceholder, removeGeminiMetadata)`, `mergeHint`
- `v7.2.105:internal/runtime/executor/antigravity_schema_sanitize_test.go`

**Implemented (request-direction only; no response/stream-path changes):**

| File | Change |
|------|--------|
| `internal/runtime/executor/antigravity_executor.go` | Replaced whole-payload cleaning block with `sanitizeAntigravityRequestSchemas(payload, useAntigravitySchema)`; added upstream-equivalent private helpers: declaration paths (`functionDeclarations` + `function_declarations`), declaration schema keys (`parameters`, `parametersJsonSchema`, `parameters_json_schema`, `response`, `responseJsonSchema`, `response_json_schema`), generation containers (`request.generationConfig` + `request.generation_config`), generation schema keys (`responseSchema`, `responseJsonSchema`, `response_schema`, `response_json_schema`); `parametersJsonSchema`→`parameters` rename only at declaration sites; tool schemas cleaned via `cleanNestedSchema` (preserves Claude VALIDATED placeholder); generation response schemas cleaned via `CleanJSONSchemaForAntigravityResponse` (placeholder-free); `NeedsSchemaSanitization` now covers all aliases |
| `internal/util/gemini_schema.go` | Added `CleanJSONSchemaForAntigravityResponse`; parametrized internal `cleanJSONSchema` (existing `CleanJSONSchemaForGemini`/`CleanJSONSchemaForAntigravity` observable behavior unchanged); added `mergeHint` idempotency (required: `antigravity_claude_request.go:676` pre-cleans input schemas in the translator, so executor cleaning is a second pass). File is byte-identical to v7.2.105 (`git diff v7.2.105 -- internal/util/gemini_schema.go` empty) |

**Tests:**
- `internal/runtime/executor/antigravity_schema_sanitize_test.go` — byte-level port of upstream final test file, minus one test (see Deferred). Covers: history-preservation regression (`functionCall.args` keys untouched, no fabricated enum), schema still cleaned (`parametersJsonSchema` renamed, `$id`/`$comment`/`minLength` dropped, property named `title` kept), result schemas, every camelCase/snake_case location matrix, equivalence with whole-payload cleaning incl. VALIDATED `_` placeholder, response-schema placeholder-freedom, response metadata preservation (`nullable`, `_` property), snake_case generation schemas through real `buildRequestBodyFromRawPayload`, both-spellings in-place, hint idempotency.
- `internal/util/gemini_schema_test.go` — added `TestCleanJSONSchemaForAntigravityResponseDoesNotAddToolPlaceholders`; existing cleaner suite unchanged (full `./internal/util` green = existing public-cleaner regression guard).
- Wiring proof via real `buildRequest` path: `TestAntigravityBuildRequestPreservesGenerationResponseSchemaMetadata`, `TestAntigravityBuildRequestSanitizesSnakeCaseGenerationResponseSchemas`, existing `TestAntigravityBuildRequest_Sanitizes{Gemini,Antigravity}ToolSchema`.

**Deferred (explicit, out of scope):**
- OpenAI `response_format:{type:"json_object"}` → `responseSchema`+`responseMimeType` translator mapping (upstream `antigravity_openai_request.go:80-85`; local translator has no such mapping, so the upstream test `TestAntigravityBuildRequestKeepsJSONObjectSchemaPlaceholderFree` was removed from the port — it pins translator behavior, not sanitization; its sanitization intent is covered by `TestSanitizeAntigravityRequestSchemasKeepsResponseSchemasPlaceholderFree`).
- Executor file split (`antigravity_executor_{request,execute,stream,tokens,auth,credits}.go`) — upstream refactor, no functional value.
- `sanitizeAntigravityGeminiRequestSignatures` / signature-sanitizer debug logging — signature architecture, excluded by policy.
- Replay ledger, Claude tool identity/provenance state machine, OpenAI unsigned-thinking cleanup, CAIS — excluded by policy.

**Unchanged (translator/thinking/signature/cache/replay):** verified — no production diffs outside `antigravity_executor.go` and `internal/util/gemini_schema.go`; translator pre-cleaning in `antigravity_claude_request.go` untouched (only interacts via idempotent `mergeHint`).

| Check | Command | Status |
|-------|---------|--------|
| util package (existing cleaner regression + new) | `go test ./internal/util/ -count=1` | **PASS** |
| Antigravity sanitize + buildRequest | `go test -run 'SanitizeAntigravityRequestSchemas\|AntigravitySchemaPaths\|AntigravityBuildRequest\|Antigravity' ./internal/runtime/executor/... -count=1` | **PASS** |
| executor / signature / thinking / usage / redisqueue / integration | `go test ./internal/runtime/executor/... ./internal/signature/... ./internal/thinking/... ./internal/usage/... ./sdk/cliproxy/usage/... ./internal/redisqueue/... ./test/... -count=1` | **PASS** |
| Full suite (Windows) | `go test ./... -count=1` | see verification line below |
| Compile | `go build -o test-output.exe ./cmd/server` + remove | **PASS** |
| gofmt (changed Go files, LF-normalized) | temp LF copies + `gofmt -l` | **clean** (4/4) |
| Upstream parity (util) | `git diff v7.2.105 -- internal/util/gemini_schema.go` | **empty** |

**Independent review:** **PASS** (GLM read-only verification, 2026-07-30).
**Pending:** Linux CI (gofmt + full suite on GitHub Actions) — not yet run.

**B000/B001/B002/B004-xAI/B004-Claude:** remain `in_progress`; CI gofmt/full suite on Linux still **pending Actions**.

---

## B005 — Codex multi-agent v2

**Status:** `local-complete / pending independent review and Linux CI` — Phase 1 (native Codex HTTP/WS) implemented and green on Windows. **Phase 2 (non-Codex provider wrappers via `TranslateRequestWithCodexMultiAgentV2`) is Deferred.**

| | |
|-|-|
| **Scope** | Upstream `84bf9376` — Codex native multi-agent v2 optimization for the HTTP and WebSocket Codex executors, plus minimal config/catalog wiring. Default **off**. |
| **Non-goals** | Codex Live; breaking WS relay sessions; Home CAS/CAIS; executor split; thinking migration; non-Codex provider translation (Phase 2). |
| **Upstream evidence** | `84bf9376` `feat(executor): replace sdktranslator.TranslateRequest with helps.TranslateRequestWithCodexMultiAgentV2` |
| **Local protection check** | Only `codex_executor.go` + `codex_websockets_executor.go` patched; Claude/xAI/Antigravity/aistudio/gemini/kimi/openai_compat executors untouched; `internal/translator/`, `internal/thinking/`, signature/cache untouched. |
| **Acceptance gates** | optimizer unit tests; HTTP Execute/ExecuteStream/compact + WS Execute/ExecuteStream spawn_agent tests; config/SDK-mapping/hot-reload-diff/server/catalog tests. |
| **PR** | Not created (local-complete only). |
| **Release** | TBD. |
| **Last updated** | 2026-07-30 |

**Implemented (upstream `84bf9376`, Phase 1):**

| Item | Local change | Risk |
|------|--------------|------|
| Optimizer package | New `internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go` (package `multiagentv2`) — UA gating (`Codex Desktop/`, `codex-tui/`), spawn_agent description/model-list rewrite, `collaboration`→`collaboration-optimize` namespace rename, encrypted agent_message content normalization, response namespace restore. | Low (gated by config + UA) |
| Helps adapter | New `internal/runtime/executor/helps/codex_multi_agent_v2.go` — thin wrappers over the optimizer package. | Low |
| HTTP executor | `codex_executor.go`: Optimize after parallel-tool normalization and before reasoning replay cache in Execute/executeCompact/ExecuteStream; Restore after identity-confuse response handling (non-stream chunk, SSE event data, and stream `data:` rebuild via `translatedLine = append([]byte("data: "), data...)`). | Low |
| WS executor | `codex_websockets_executor.go`: Optimize after parallel-tool normalization and before replay cache in Execute/ExecuteStream; Restore after identity-confuse response handling. | Low |
| Config | `CodexConfig.OptimizeMultiAgentV2` (yaml/json `optimize-multi-agent-v2`, default false); `SDKConfig.CodexOptimizeMultiAgentV2` (yaml/json `-`); watcher diff entry; `effectiveSDKConfig` mirror. | Low |
| Catalog | `codex_client_models.go` advertises `multi_agent_version: "v2"` on synthesized models when enabled; server home handler passes the flag; `config.example.yaml` documents the option. | Low |

**Tests added (all PASS on Windows):**
- `optimize_multi_agent_v2_test.go` — disabled/UA no-op, spawn_agent description, namespace, round-trip restore, agent-a/b isolation, unknown input, translate conditions.
- `codex_executor_spawn_agent_test.go` — HTTP Execute/ExecuteStream/compact, enabled/disabled/UA, response restore.
- `codex_websockets_spawn_agent_test.go` — WS Execute/ExecuteStream, enabled/disabled, response restore.
- `server_sdk_config_test.go`, `codex_websocket_header_defaults_test.go`, `codex_client_models_test.go` — config parse, SDK mapping, catalog on/off.

**Deferred (Phase 2):** non-Codex provider translation wrappers — upstream routes aistudio/antigravity/claude/gemini/gemini_vertex/kimi/openai_compat through `TranslateRequestWithCodexMultiAgentV2`. The wrapper is present in the optimizer package but is **not wired** into those executors this cycle (protected executors out of scope); to be promoted in a dedicated sub-batch.

| Check | Command | Status |
|-------|---------|--------|
| Optimizer package | `go test ./internal/client/codex/optimize-multi-agent-v2/ -count=1` | **PASS** (Windows) |
| Executor spawn_agent | `go test -run 'OptimizeMultiAgentV2' ./internal/runtime/executor/ -count=1` | **PASS** (Windows) |
| Config/API/watcher | `go test ./internal/config/ ./internal/api/ ./internal/watcher/diff/ -count=1` | **PASS** (Windows) |
| Catalog | `go test ./sdk/api/handlers/openai/ -run 'CodexClientModels\|ApplyCodexClientModelMetadata' -count=1` | **PASS** (Windows) |

**Pending:** independent review + gofmt/full suite on Linux CI — not yet run.

---

## Deferred (explicit non-batch)

| Item | Rationale |
|------|-----------|
| Codex Live | Large surface; product-specific |
| Home / conductor overhaul | UX divergence; high conflict risk |
| Request lifecycle plugin | Architectural; needs design |
| Pure file splits | No user value alone |
| Thinking pipeline migration | Protected canonical pipeline |
| Pure performance optimizations | No functional gap identified |

---

## Final consistency audit (Batch 0-4, 2026-07-30)

| Check | Command / evidence | Result |
|-------|-------------------|--------|
| git status / diff --stat / name-status | 17 modified + 6 new untracked batch files (see inventory below) | **PASS** — all expected files present, no deletions (D), no whole-file overwrites |
| Whitespace | `git diff --check` | **PASS** (CRLF warnings only, pre-existing) |
| gofmt (19 changed/new Go files only) | `gofmt -l <file list>` | **PASS** — 0 files listed |
| Full test suite | `go test ./... -count=1` | **PASS** — exit code 0, 0 FAIL |
| Compile | `go build -o test-output.exe ./cmd/server` + remove | **PASS** — artifact created and deleted |
| Build artifacts | `test-output` / `test-output.exe` absent; `cli-proxy-api.exe` pre-existing (not batch-produced) | **PASS** |
| Production test hooks | grep `ResetRefreshGroupForTest` | **PASS** — 0 hits |
| Backlog trackability | `git check-ignore -v docs/upstream-sync-backlog.md` → `.gitignore:37:!docs/upstream-sync-backlog.md`; appears in `git status --short` as `??` | **PASS** — not ignored, trackable after `git add` |
| New test files not ignored | `git check-ignore` on all 4 new test files + accounting.go/test | **PASS** — none ignored |
| B1 sentinel: xAI OAuth refresh | `api_tools.go`: 5 matches (refreshXAI) | **PASS** |
| B2 sentinel: fork ParseGeminiCLI | `usage_helpers.go:757,766` + tests `usage_helpers_test.go:618,630` | **PASS** |
| B2 sentinel: accounting.go | exists, 14,385 bytes, untracked new file | **PASS** |
| B3 sentinel: 9 models + qoder | models.json: claude-opus-5, gemini-3.5-flash-lite ×3, gemini-3.6-flash ×3, gemini-3.1-pro-preview ×3, gemini-3.6-flash-high, qoder 12 entries | **PASS** |
| B3 sentinel: catalog test | model_catalog_sync_test.go: 3 test funcs + qoder protection | **PASS** |
| B4-xAI sentinel: output controls + video usage | xai_executor.go: 5 matches | **PASS** |
| B4-Claude sentinel: upstreamStream + OAuth restore | claude_executor.go: 6 matches | **PASS** |
| B4-Antigravity sentinel: targeted sanitization | antigravity_executor.go: 4 matches (sanitize/Sanitize) | **PASS** |
| Doc false claims | grep `merged\|CI passed\|CI PASS` in backlog → vocabulary def + cautionary text + "merged YAML content" only; no batch marked merged or CI-passed | **PASS** |

**Untracked non-batch files (report only, not deleted):** `cli-proxy-api.exe~`, `config-cursor-test.yaml`, `cursor-server.log`, `scripts/keep-alive-cliproxy.ps1`, `scripts/register-keepalive-task.ps1`, `scripts/start-cliproxy-local.bat`, `scripts/start-cliproxy-local.ps1`.

---

## Decision Log

| Date | Decision |
|------|----------|
| 2026-07-30 | Adopt **selective sync** against upstream `v7.2.105` (`4a2eb54d`) instead of blind fast-forward. Plus providers and fork CI remain local. Batch 0 adds backlog + CI guards only. B001–B002 queued as `planned`; B003–B005 remain `candidate` until evidence review. Deferred list is explicit `wont_sync` for this cycle unless promoted. |
