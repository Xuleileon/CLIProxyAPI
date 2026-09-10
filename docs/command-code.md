# Command Code

CPA supports Command Code subscription credentials as a native `command-code`
provider. Requests use the CLI `/alpha/generate` bridge, not the paid Provider
API. No separate reverse-proxy process is required.

## Configuration

```yaml
force-model-prefix: true
command-code-api-key:
  - name: Command Code
    api-key: YOUR_COMMAND_CODE_KEY
    prefix: command-code
    # proxy-url: http://127.0.0.1:7897
```

Use `force-model-prefix: true` to isolate prefixed providers from unprefixed
requests. A prefix alone registers both forms by default: without this setting,
an unprefixed model such as `claude-fable-5-1` can select Command Code or fall back
to it after another provider fails. The setting applies globally to accounts with
prefixes; accounts without a prefix keep their existing model IDs. Check other
prefixed accounts before enabling it on an existing installation.

Restart CPA after upgrading the binary. Subsequent account changes hot-reload.
The AI Providers workbench supports adding, editing, disabling and deleting
Command Code accounts. The management API is `/v0/management/command-code-api-key`
(GET, PUT, PATCH, DELETE), using the existing management authentication.

Accounts support `priority`, `base-url`, `proxy-url`, `headers`, `models`,
`excluded-models`, and `disable-cooling`. The default base URL is
`https://api.commandcode.ai`; do not append `/v1` or `/alpha/generate`.
Optional model mappings use `name` (full upstream ID), `alias`, `display-name`,
and `force-mapping`, following CPA's existing API-key model configuration.

With no explicit model list, CPA loads the complete public Command Code catalog.
The catalog does not carry account entitlements: a listed model is not proof
that the current subscription can call it. In particular, Go includes some
closed-model exceptions, so CPA does not filter models by vendor. Account
entitlements are enforced upstream. The official 2026-09-10 snapshot (69 models)
is embedded as an offline fallback.
`--local-model` disables remote catalog refresh. A failed or empty refresh keeps
the last good catalog. Explicit account models override the default catalog.

Example client model IDs with the prefix above:

- `command-code/meituan/LongCat-2.0:free`
- `command-code/deepseek/deepseek-v4-flash`

Clients can use `/v1/chat/completions`, `/v1/messages`, or `/v1/responses` in
streaming and non-streaming mode. CPA preserves text, reasoning, function calls,
tool results, finish reasons, and input/output/cache/reasoning token accounting.
Thinking options pass through CPA's canonical thinking pipeline. Upstream
reasoning support remains model-specific.

## Protocol behavior and limitations

- The upstream always streams. HTTP 200 is not sufficient for success: a
  successful `finish` event is required. Stream errors, malformed tool arguments,
  and premature EOF propagate as failures. CPA does not invent a stop event.
- Validated tool calls are emitted once. The client executes tools and submits
  their results. CPA does not execute tools on the Command Code server.
- Session identifiers are stable, account-scoped UUIDs when the client supplies
  a session header or CPA session metadata; otherwise each request gets a UUID.
- No provider-level retries or generation deadlines are added. Existing CPA
  routing/retry configuration still applies; avoid speculative request replay.
- The bridge injects Command Code's own CLI system prompt. CPA does not add
  compensating prompts, prune history, or claim exact system-prompt equivalence
  with the official Provider API. Unsupported content parts fail explicitly.
- The default `x-command-code-version` is `1.53.0`, verified on 2026-09-10.
  Override it with the account's `headers` when an upstream CLI update requires
  a newer version. An internal protocol can change without notice.
- The official `/provider/v1/*` endpoints reject Go accounts with
  `403 upgrade_required`, including free-model requests. CPA does not silently
  switch to a differently billed endpoint.
- Native management integration recognizes the supported legacy and v1.22.2-1
  management bundles. Unknown bundle versions are left intact; configuration
  and management API remain available.

## Verification and references

Validated through an isolated CPA on 2026-09-10: LongCat text and reasoning on
all three API formats, streaming tool calls through Messages and Responses,
and a DeepSeek V4 Flash Chat Completions tool round trip. Each round trip used
a fresh tool result generated after the model's call and verified the final
answer matched it. Laguna returned an upstream stream error during the initial
probe; this was not counted as success. Paid-model checks used only a small
number of DeepSeek Flash requests.

Protocol research used these independently maintained implementations; CPA's
adapter is implemented in Go against the observed wire protocol:

- https://github.com/MAXeaglet/commandcode-proxy
- https://github.com/thaolaptrinh/commandcode-api-proxy
- https://github.com/hayou2002/command-code-proxy
- https://github.com/HaeMeto/Command-Code-AI-Proxy
- https://commandcode.ai/docs/provider

No subscription credential belongs in source control or test fixtures.
