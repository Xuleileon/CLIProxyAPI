package claude

import (
	"context"

	codex "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToResponses shares the message and tool mapping with Codex,
// but preserves HTTP generation controls and omits Codex-only defaults.
// Anthropic context-management edits have no equivalent in Responses compaction.
func ConvertClaudeRequestToResponses(model string, raw []byte, stream bool) []byte {
	out := codex.ConvertClaudeRequestToCodex(model, raw, stream)
	for _, field := range []string{"store", "include"} {
		out, _ = sjson.DeleteBytes(out, field)
	}
	if !gjson.GetBytes(raw, "thinking").Exists() && !gjson.GetBytes(raw, "output_config.effort").Exists() {
		out, _ = sjson.DeleteBytes(out, "reasoning")
	}
	if effort := gjson.GetBytes(raw, "output_config.effort"); effort.Exists() {
		out, _ = sjson.SetRawBytes(out, "reasoning.effort", []byte(effort.Raw))
	}
	for _, field := range []string{"temperature", "top_p"} {
		if value := gjson.GetBytes(raw, field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}
	if limit := gjson.GetBytes(raw, "max_tokens"); limit.Exists() {
		out, _ = sjson.SetRawBytes(out, "max_output_tokens", []byte(limit.Raw))
	}
	out, _ = sjson.SetBytes(out, "stream", stream)
	return out
}

// ConvertResponsesToClaudeNonStream wraps the native response in the terminal
// event expected by the shared Codex response converter.
func ConvertResponsesToClaudeNonStream(ctx context.Context, model string, originalRequest, request, raw []byte, param *any) []byte {
	if gjson.GetBytes(raw, "object").String() == "response" {
		event := []byte(`{"type":"response.completed"}`)
		if gjson.GetBytes(raw, "status").String() == "incomplete" {
			event = []byte(`{"type":"response.incomplete"}`)
		}
		raw, _ = sjson.SetRawBytes(event, "response", raw)
	}
	return codex.ConvertCodexResponseToClaudeNonStream(ctx, model, originalRequest, request, raw, param)
}
