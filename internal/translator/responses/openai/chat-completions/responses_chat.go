package chat_completions

import (
	"context"

	codex "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToResponses reuses the shared Responses message and tool
// mapping without applying Codex-only generation restrictions or defaults.
func ConvertOpenAIRequestToResponses(model string, raw []byte, stream bool) []byte {
	out := codex.ConvertOpenAIRequestToCodex(model, raw, stream)
	if !gjson.GetBytes(raw, "reasoning_effort").Exists() {
		out, _ = sjson.DeleteBytes(out, "reasoning.effort")
	}
	for _, field := range []string{"temperature", "top_p", "parallel_tool_calls", "store", "metadata", "service_tier", "user", "prompt_cache_key"} {
		if value := gjson.GetBytes(raw, field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}
	limit := gjson.GetBytes(raw, "max_completion_tokens")
	if !limit.Exists() {
		limit = gjson.GetBytes(raw, "max_tokens")
	}
	if limit.Exists() {
		out, _ = sjson.SetRawBytes(out, "max_output_tokens", []byte(limit.Raw))
	}
	return out
}

// ConvertResponsesToOpenAINonStream accepts the native HTTP response object;
// Codex's converter otherwise expects that object inside a terminal SSE event.
func ConvertResponsesToOpenAINonStream(ctx context.Context, model string, originalRequest, request, raw []byte, param *any) []byte {
	if gjson.GetBytes(raw, "object").String() == "response" {
		event := []byte(`{"type":"response.completed"}`)
		if gjson.GetBytes(raw, "status").String() == "incomplete" {
			event = []byte(`{"type":"response.incomplete"}`)
		}
		raw, _ = sjson.SetRawBytes(event, "response", raw)
	}
	return codex.ConvertCodexResponseToOpenAINonStream(ctx, model, originalRequest, request, raw, param)
}
