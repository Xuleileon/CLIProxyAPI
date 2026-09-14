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
- Final deployed code revision: `93419268` plus preserved existing usage changes. Production Codex tool turns completed in 2.50 and 2.98 seconds; Muse tool turns completed in 4.00 and 13.72 seconds. All four responses ended with `response.completed`, and both tool-result checks passed.
- Final executable SHA-256: `14E22F85510653A05D3D95C9CA88DE3BB17B7B2A447CAA6EF4AA31871D34723D`. The original port is 18317, configuration is unchanged, and all nine production auth files remain present. The isolated staging process has been stopped.

## Follow-up: reuse completed connections without sharing active streams

Repeated connection acquisition EOF remained after the first repair. Always
opening a new connection exposed every request to that unreliable handshake.
The follow-up replaces the per-request discard policy with exclusive transport
leases. A response that reaches EOF or a parsed terminal SSE event can return
its transport for reuse after body close; canceled, truncated, failed, or
prematurely closed streams discard their lease. Active requests never share
the same HTTP/2 transport. A bounded cache keeps at most four idle transports
per original transport and evicts old pools without interrupting active work.
Custom transports, proxy settings, request retry budgets, and streaming
deadlines are unchanged. No Clash routing or node configuration was changed.

Validation:

- A local HTTP/2 server verifies healthy reuse, concurrent stream isolation,
  canceled/truncated body eviction, terminal-SSE close without waiting for body
  EOF, and late lease return after pool eviction.
- Focused executor, helper, auth, and handler packages pass. The pool tests also
  pass Go's race detector.
- Four old-version requests completed in 11.78, 23.14, 3.11, and 5.14 seconds.
  Four isolated follow-up requests completed in 12.81, 2.17, 7.98, and 4.75
  seconds. This small sequential sample is not a general latency benchmark.
  The follow-up logs directly confirm reused HTTP/2 connections with zero new
  TLS handshake time after the first request.
- The isolated tool round trip completed in 4.78 and 1.91 seconds, with both
  responses ending in `response.completed` and the returned tool value verified.
- Initial connection acquisition can still fail; reuse reduces exposure to the
  failure rather than proving the underlying network/provider issue is gone.

Deployment of the follow-up:

- Code revision `ea78c5f5` plus the preserved existing usage changes is running
  on port 18317. Executable SHA-256 is
  `795C081FA5C6E2ECC929CDC2F03571C2CAA0EBF6198876DDBC4C23C6268DF7C3`.
- Configuration hash is unchanged and all nine production auth files remain.
  The isolated test process was stopped and its temporary config removed.
- Production Muse tool turns completed in 4.53 and 1.83 seconds, with the
  second turn logging `reused=true` and `tls_done_ms=0`. Both returned terminal
  completion events and the tool result check passed. Codex also completed in
  3.33 seconds.
- The short post-restart observation contained five HTTP 200 completions and
  no logged OpenCode transport failures. This is an acceptance observation,
  not a long-term availability claim.


## TLS timeout classification follow-up

A real local TCP listener that accepts but never completes TLS reproduced Go's private `http.tlsHandshakeTimeoutError`. It did not match context deadline or net.OpError checks and previously made a healthy model unavailable for approximately 60 seconds.

The fix recognizes typed network timeouts and socket operation failures, preserves an explicit executor transport marker across local 502 conversion, and decouples acquisition retry hints from credential cooldown state. Submitted requests remain request-scoped and cannot be replayed. Explicit upstream 401/403/429/502 responses retain existing cooldown semantics. OpenCode failure summaries now include the root error type without logging URLs, credentials, or payloads.

Validation: real TLS timeout regression; raw and marked gateway classification; explicit upstream status controls; complete OpenCode executor retry-budget, cancellation/submission and no-cooldown assertions; helper and logging packages; targeted auth/Codex/OpenCode tests; server build. Existing unrelated usage changes remain present in the local binary and excluded from this commit. Codex WebSocket routing is unchanged; no claim is made that upstream protocol failures have been eliminated.

Deployment verified at 23:28:53 Asia/Shanghai: source 967ed7da plus preserved existing usage changes; PID 59284; SHA256 1375DC260761F1FEDE6CBD4FA05F44141CAC63B7A4197FA0DB49045714C92F0A. /livez returned 200, deployed bytes matched candidate, configuration hash unchanged and nine auth files retained. Muse tool roundtrip completed in 5.77s/1.56s; Codex gpt-5.6-sol in 9.41s/2.16s. All four responses reached response.completed and both tool return values were verified.
