# OpenCode Go latency and replay boundaries

## Behavior

OpenCode Go transport failures are now classified by HTTP connection acquisition. Only failures before a connection is handed to the request get a 250 ms retry hint within the existing manager budget. A disconnect after acquisition is request-scoped because the upstream may already have received the inference. No request or first-token timeout was added.

Headerless temporary 429 responses use a shared per-credential/model 2, 4, 8, 16, 30 second backoff. Concurrent failures reuse the active window. Explicit Retry-After values keep priority; subscription-exhaustion errors retain the quota path. Other providers retain their existing retry floors.

OpenCode attempts advertise X-CPA-Retry-Handled to prevent a cooperating client from multiplying a completed CPA retry budget. The Cursor++ OpenAI adapter honors this signal and carries an opaque conversation identity across both Chat Completions and Responses requests, including compaction. Explicit custom session headers keep precedence.

Transport/retry logs report connection reuse, TLS completion, request upload completion, first byte, status, and scheduled wait without credential or prompt text. This distinguishes network delay, upload, retry backoff, and upstream response time.

## Validation before replacement

- Focused OpenCode executor/helper/auth tests passed, including fault-injected pre-send recovery, submitted-request non-replay, unchanged body/session, and total attempt bounds.
- Cursor++ model adapter package passed, including both wire paths across changed first-message/compaction and single-layer retry ownership.
- Real candidate requests through Cursor++'s model adapter and CPA port 18318 retained all five archive markers in two 300k-context turns for both Muse and Cursor Gemini.
- Muse continuation returned 293731 cached tokens but also encountered two upstream 429s before success. Cache reuse does not guarantee low latency when upstream capacity is limited.
- Gemini continuation sent about 1.248 MB, preserving the complete archive through the already-committed cold-history fix.
- The original checkout's full test command encountered ignored temporary Go fixtures/source copies and timing-sensitive tests. The timing-sensitive tests passed independently in a clean checkout. A clean full-source validation is recorded with the release evidence.

## Release evidence

Local artifacts: `tmp/opencode-latency-release-20260908/` (candidate results, sanitized timeline, builds, test logs, source snapshots, and deployment receipts). The existing local proxy 127.0.0.1:7897 was tested only as an OpenCode-specific route; global network policy is unchanged. Preserve existing reasoning levels and full history rather than trading correctness for benchmark speed.
