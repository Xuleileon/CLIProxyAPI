# Provider routing and Antigravity Gemini repair

## Confirmed failures

- At 22:28:28 on 2026-09-10 (Asia/Shanghai), request `66754a91` selected
  Claude OAuth for `claude-fable-5-1`. After an upstream TLS EOF, the same
  unprefixed name selected Command Code and received `MODEL_NOT_IN_PLAN`.
  A configured prefix alone registers both prefixed and unprefixed names
  unless `force-model-prefix` is enabled.
- Antigravity request `bc1ec8b2` sent `gemini-3.8-flash-high` to Google's
  `streamGenerateContent` endpoint and received 404. The authenticated live
  catalog advertises `gemini-3.8-flash-tiered`, not that static high ID.
- After correcting the model mapping, direct Antigravity generation still
  encountered TLS EOF. Using the existing local proxy for that account
  allowed generation to complete. This does not establish the underlying
  cause of the direct connection failures.

## Runtime configuration repair

The local production configuration now uses `force-model-prefix: true`.
Command Code is the only configured account with a prefix, and the OAuth
accounts have no prefixes. Its 69 unprefixed registrations were removed;
all 89 pre-integration model IDs remained available.

Command Code requests must use `command-code/<upstream-model-id>`.
The isolated test prefix `cc/` is not configured in production.

An explicit Antigravity-only compatibility alias preserves the existing
client model setting:

```yaml
oauth-model-alias:
  antigravity:
    - name: gemini-3.8-flash-tiered
      alias: gemini-3.8-flash-high
      fork: true
```

The active Antigravity account uses the existing local HTTP proxy. No Clash
configuration or process was changed. Request `960d57d1` then completed on
the production Responses endpoint with the requested marker, a real
`response.completed` event, and 13 input / 8 output tokens. Its upstream
model was `gemini-3.8-flash-tiered`.

## Source repair and validation

Antigravity model discovery now reads the account's actual models and
advertised tiered generation IDs. When a static Gemini high ID is absent
and the corresponding tiered ID is present, discovery registers the live
ID instead. It retains unrelated models in partial catalogs and does not
publish internal tab/chat model IDs. Token, thinking, modality and search
capabilities follow the discovered metadata.

The service retains the last successful catalog per account, project and
upstream base URL when a later fetch fails or returns an incomplete body.
Explicit compatibility aliases remain separate from model discovery.

Validation passed:

- `go test ./sdk/cliproxy/... ./internal/runtime/executor/... ./test -count=1`
- Regression tests for account/project cache isolation, partial catalogs,
  tiered metadata and forced-prefix cross-provider fallback prevention.
- A server build and a real production Gemini Responses request through
  the existing client alias.

The production configuration repairs are live. The additional discovery
source changes require installing the new binary; process-start approval
was blocked during this run, so they must not be described as deployed yet.
