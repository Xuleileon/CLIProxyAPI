# Context and tool integrity upstream audit — 2026-09-08

## Scope and source snapshots

This batch repairs demonstrated context loss, tool identity loss and false completion. It is not a claim of complete upstream parity.

- Local base: `aba59a9a75b2b338d50e475e45730baaba662a1c` (complete Cursor cold history).
- CPA: `router-for-me/CLIProxyAPI`, `d198db54d4c4886c99b21488d54fc576933019a3`.
- CPA Plus: `kaitranntt/CLIProxyAPIPlus`, `f5570ed69c3b82e3ec789b986a7f61396af49180`.

## Why the earlier sync missed truncation

The September 7 sync, `c3a92d7d`, explicitly preserved local Cursor and OpenCode behavior. Its staging checks excluded those paths. That protected local implementations but also excluded their behavior from the upstream audit.

The local August 21 `.cursor-gap-synthesis.md` already identified the 8 KB history limit. Local commit `4f6381ce` introduced that limit after Plus `08fed280` had addressed cold continuation. The issue remained an architectural backlog item; the tests asserted truncation instead of verifying complete long-history replay. This was a scope and acceptance gap, not simply an upstream timing issue. Some September 8 CPA fixes below did arrive after the previous sync.

Future acceptance for this path must check retained content at the serialized RunRequest boundary and recover known facts from a cold continuation. A protected path or successful merge is not behavioral validation.

## Applied changes

| Boundary | Upstream reference | Local repair and evidence |
| --- | --- | --- |
| Cursor cold history | Plus `08fed280`, `ca97fbeb` | Preceding local `aba59a9a` removes the 8 KB cap. Large history, tool arguments/results and wire regressions; live 300k-token archive recovered 6/6 constants. |
| Cursor frame decoding | Plus `d2aea015` | A trailing interaction update cannot overwrite an already decoded reply-required request. KV get/set, read and local precompact cases failed before and pass after. |
| Cursor KV acknowledgement | Audit of the same reply boundary | Propagate KV reply write errors instead of reporting a successful run. Actual frame-loop regression failed before and passes after. |
| Claude mixed tool results | CPA `8564142f` | Reorder tool results while keeping non-result blocks in their original slots. Common and provider integration regressions. |
| Gemini function responses | CPA `f6d19a32` | Normalize missing/invalid function-response roles to user. Regression failed before and passes after. |
| Responses tool namespaces | CPA `03a054e3` | Recover omitted namespaces only for unambiguous names; preserve exact matches and ambiguity. Request/response translation tests. |
| Codex collaboration tools | CPA `82f4f370` | Restore dotted collaboration names and preserve prefix-conflict guards. Optimizer and executor regressions. |
| Claude agent messages | CPA `d2f71220` | Preserve Codex agent-message text in standard, compatibility and mixed inputs. All three cases failed before and pass after. |
| Gemini content boundaries | CPA `5dc428f3` | Ensure leading/trailing user content for Gemini, Vertex, AI Studio and Gemini-through-Antigravity generation. Preserve count-token inputs, function responses and Claude targets. Actual Gemini HTTP capture and AI Studio translation failed before and pass after; helper tests cover boundary preservation. |

## Related Plus dispositions

- `453b7b2d`: last-good model cache already has local implementation and tests for repeated failure, mutation isolation and concurrent auth isolation.
- `9543be5f`: resolved model override already has a local RunRequest regression.
- `954ac6cb`, `2d312f72`, `55182469`, `08b70103`: local streaming, pending tool sessions, checkpoints and terminal-error handling differ substantially. Existing local executor regressions were retained and run; replacing the executor wholesale would discard these contracts.
- `d2aea015`: adopted the proven decoding correction, not its post-connect idle/flow deadlines. Those deadlines conflict with this repository's network-timeout rule.
- Plus usage publishing remains a follow-up: the local continuous run spans multiple HTTP legs and uses delta accounting. A simple upstream reporter copy would risk duplicate or misattributed usage. This batch does not claim that observability gap is fixed.

## Other reviewed CPA changes left outside this batch

Codex bootstrap retries (`ba7e5583`), tool-name sanitization (`bee20b99`), strict defaults (`d01516c1`), service-tier/cache accounting (`8696585c`), complex schema/empty-incomplete handling (`bf20b999`), wsrelay terminal queuing (`7871a5a9`), auth cooldown changes (`1c22598d`), Gemini signature presentation (`510c9c8f`), Antigravity tool-choice handling (`a76da711`) and transport changes (`d5397905`) require their own local failure evidence and integration review. They are not marked as applied or verified equivalent. Compaction, structured-output and plugin wire-profile additions are features, not repairs included here.

## Validation and deployment limits

- All changed functional packages passed full package tests, including Cursor protocol/executor, Gemini helpers, translation and Codex optimizer tests.
- Server compilation succeeded.
- Broad `go test ./... -skip '^TestGetPluginSyncCancellationInterruptsRead$'` passed all packages except the management package's `TestDeletePluginRemovesDiscoveredFileAndConfig` (5-second config-reload wait). A full uncached management-package rerun then passed in 23 seconds. The excluded Home test was independently reproduced on unchanged `b2a05498`. These exceptions must remain visible in release reporting.
- Gemini HTTP capture and translation tests exercise local outbound behavior; they are not live acceptance against every provider account. Vertex and Antigravity integration sites were inspected, but not independently accepted with live credentials.
- Runtime replacement must be distinguished from a source commit, build or isolated test. Do not interrupt active production continuations to replace the executable.
