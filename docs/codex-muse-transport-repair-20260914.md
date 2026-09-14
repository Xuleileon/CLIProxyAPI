# Codex and Muse transport repair

## Evidence

- Production Codex request `e991b58d` failed on 2026-09-14 at 20:25:26 with an HTTP/2 peer `PROTOCOL_ERROR`. Fourteen subsequent requests returned local `model_cooldown` in 22 seconds. This is not evidence of an upstream quota rejection or a new account restriction.
- Muse failures repeatedly used an existing connection, received no first response byte, and ended after caller cancellation. A fresh direct request completed in 6.66 seconds; two fresh Go HTTP/2 probe requests completed in 4.74 and 2.57 seconds. The old production path remained stalled during the comparison.
- An isolated repaired CPA completed a Muse response in 7.25 seconds and a two-request tool round trip in 9.33 plus 3.61 seconds. Codex `gpt-6-astra` completed in 2.27 seconds after the live model catalog loaded.
- A probe with a named tool choice received an explicit upstream 400: Muse currently supports only `tool_choice: auto`. The successful tool round trip used `auto`; production request semantics are not rewritten.

## Changes

- Classify typed HTTP/2 stream, GOAWAY, and connection errors as connection lifecycle failures instead of cooling credentials. Real status-bearing authentication and quota errors remain authoritative.
- Isolate standard HTTP transport pools for OpenCode Go Responses requests while preserving TLS/proxy configuration and custom transports. HTTP/2 stays enabled. Connections are not reused across requests, which adds connection setup cost but prevents an old shared connection from poisoning later requests. No post-connection timeout or speculative inference replay is added.
- Keep post-connection OpenCode HTTP/2 errors non-replayable, and log error types and cancellation state without arbitrary transport error text.
- Expose local cooldown `Retry-After` through the trusted-header path even when generic upstream-header passthrough is disabled.
- Exclude launcher-owned stdout/stderr files from log deletion.

## Upstream review

- `router-for-me/CLIProxyAPI`, MIT, reviewed main `7fa443dc`: backported the targeted model-capacity classification from [3ae9093d](https://github.com/router-for-me/CLIProxyAPI/commit/3ae9093da837c32667d2f2b65e8ad448fdf1bb8f). No unrelated provider updates were merged.
- `kaitranntt/CLIProxyAPIPlus`, MIT, reviewed main `e197b6c0`: [8ea809c4](https://github.com/kaitranntt/CLIProxyAPIPlus/commit/8ea809c4) adds OpenCode session headers. This fork already supplies those headers for native Responses requests, including conversation identity and caller scope, so the overlapping generic-provider patch was not imported.
- Original upstream `aedc9e6a` adds terminal authentication handling across twelve files. It was reviewed as a separate improvement, not imported wholesale into this transport repair. No permanent authentication rejection was established in the incident.
- Related project reports include [OpenCode Muse hangs](https://github.com/anomalyco/opencode/issues/45053), [Hermes Responses routing](https://github.com/NousResearch/hermes-agent/issues/102148), and [CPA HTTP/2 protocol errors](https://github.com/router-for-me/CLIProxyAPI/issues/4234). These reports do not independently establish the cause of this machine's failure. The local Muse route already uses Responses.
- [Official Codex releases](https://github.com/openai/codex/releases) were checked. No verified announcement establishing a recent account-risk change was found. This repair does not spoof client identity, bypass challenges, change egress identity, or suppress policy errors.

## Validation scope

Focused auth, executor, executor-helper, logging, and API-handler packages pass. Regression coverage includes HTTP/2 connection separation, non-replayable resets, credential cooldown invariants, trusted retry headers, launcher logs, and upstream capacity classification.

The workspace-wide test command is not green: historical files under `tmp/` and `logs/releases/` fail compilation, the byte-write audit scans old nested worktrees, the Home cancellation test exceeds its timing expectation, and one concurrent Antigravity pool test failed under the broad run while the focused executor package passed. These areas are not modified by this repair. Live transport success is reported separately from full-suite status.

The existing four modified usage-reporting files and associated untracked helpers predate this repair and are preserved. Deployment must retain that existing workspace behavior and keep this repair's commit scoped to its own files.

## Production verification and follow-up

- The first replacement matched the candidate hash, preserved the configuration hash and all nine auth files, and restarted through the original scheduled watchdog.
- Production Muse completed a tool round trip in 8.05 and 3.27 seconds. Larger existing Muse requests also reached response headers over fresh HTTP/2 connections.
- Production Codex completed a simple response in 3.86 seconds, but tool-round-trip verification exposed intermittent TLS-handshake EOF before sending the request. These failures did not produce the former credential cooldown cascade.
- A follow-up allows at most two EOF reconnects during Codex connection acquisition, before submitting any HTTP request. It does not retry post-submission failures, alter TLS identity, or add stream deadlines. Regression tests verify the bounded attempts and that the request body remains unread.
- Formal-source testing confirms the remaining unrelated Home cancellation and nested-directory byte-write-audit failures; the affected packages pass focused tests.
