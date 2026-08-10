# OpenCode Go Management Design

## Goal

Provide native, end-to-end management for one or more OpenCode Go subscription accounts in the existing management panel. A saved account must be visible and independently manageable on the AI Providers page, persisted in the active YAML configuration, loaded into the runtime scheduler, and associated with the built-in OpenCode Go model catalog.

## First Principles

1. `opencode-go-api-key` is the single source of truth.
2. Each array entry is one independently scheduled account.
3. OpenCode Go credentials are configuration-backed API keys, not JSON or OAuth credential files.
4. The UI may mask a secret, but must not invent a second credential representation.
5. Every mutation must preserve unrelated fields and must be observable after the runtime hot reload.

## User Experience

### AI Providers

Add an `OpenCode Go` provider family to the existing AI Providers page. The family count is the number of configured accounts. Each account is rendered as a separate resource row with:

- optional account label;
- masked API key;
- active/disabled state;
- default or configured base URL;
- priority and model prefix;
- model, header, and excluded-model counts;
- runtime auth index when loaded.

The page supports creating, viewing, editing, replacing the key, enabling/disabling, and deleting one account without rewriting or losing other accounts.

### OAuth Login

Keep the OpenCode Go quick-entry card under "Other login methods" because users already discover credentials there. Saving from this card appends a new account and never overwrites existing accounts. Duplicate keys are rejected or treated as already configured. A successful save invalidates the management configuration cache so the account appears on AI Providers after navigation or refresh.

### Auth Files

Do not render OpenCode Go accounts on Auth Files. That page remains the source of truth for file-backed and OAuth credentials. This avoids a virtual JSON file that could disagree with the YAML configuration.

## Configuration Model

Extend each `OpenCodeGoKey` entry with an optional `name` field used only as a human-readable account label. Existing configurations remain valid. Empty names fall back to `OpenCode Go #N` in the UI.

The existing fields remain independently editable:

- `api-key`
- `name`
- `priority`
- `prefix`
- `base-url`
- `proxy-url`
- `models`
- `headers`
- `excluded-models`
- `disable-cooling`

The runtime identity and scheduling behavior continue to use the existing synthesized auth/client path. One configuration entry produces one auth client. Multiple entries therefore participate in the configured round-robin or priority strategy and retain per-client health/cooling state.

## API Behavior

Use the existing management collection endpoint:

- `GET /v0/management/opencode-go-api-key`
- `PUT /v0/management/opencode-go-api-key`
- `PATCH /v0/management/opencode-go-api-key`
- `DELETE /v0/management/opencode-go-api-key`

Mutations identify the current item by array index from the latest read. PATCH updates only supplied fields. An empty API key during edit means "keep the existing secret" at the UI layer. DELETE removes only the selected index. The API response includes the runtime `auth-index` when the synthesized client is present.

Enable/disable uses the existing API-key disable mechanism so the UI state and scheduler state agree. The OpenCode Go key itself is shown only in masked form in rendered UI.

## Management Asset Integration

The repository ships a compiled management page rather than its source project. Extend the existing deterministic management-asset patcher with tightly scoped anchors for:

- configuration parsing and cache updates;
- OpenCode Go CRUD client methods;
- provider-family definition and resource mapping;
- provider form metadata and translations;
- the existing OAuth quick-entry cache invalidation.

The patch must be idempotent and fail closed if the expected upstream bundle anchors change. Tests must exercise both a representative fixture and the current bundled `static/management.html`.

## Error Handling and Safety

- Never log API keys or include them in errors.
- Reject empty keys when creating an account.
- Preserve fields unknown to the UI when updating an entry.
- Confirm destructive deletion in the existing provider sheet flow.
- If the compiled bundle changes and anchors no longer match, leave it unmodified and log a diagnostic message rather than corrupting the asset.

## Verification

1. Unit tests prove configuration parsing and management CRUD for multiple accounts.
2. Asset patch tests prove idempotence and the presence of all OpenCode Go provider-page integration anchors.
3. Go formatting, targeted tests, full compile, and relevant package tests pass.
4. A disposable configuration proves add/edit/disable/delete without touching the user's real key.
5. The formal service on port 18317 is rebuilt and restarted with `config-cursor-test.yaml`.
6. The formal AI Providers page shows the user's existing OpenCode Go account, masked key, runtime status, and model count.
7. Logs prove the configuration reload and registered OpenCode Go client/model catalog without exposing the secret.

## Non-goals

- Creating synthetic auth JSON files.
- Implementing OpenCode account OAuth.
- Persisting quota or billing data not exposed by the OpenCode Go upstream.
- Replacing the management frontend build system.
