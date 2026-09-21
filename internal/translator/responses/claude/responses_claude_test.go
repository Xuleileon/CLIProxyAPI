package claude

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeResponsesGenerationControls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		out := ConvertClaudeRequestToResponses("muse-spark-1.3-contributor", []byte(`{"max_tokens":512,"temperature":0.3,"top_p":0.9,"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}}`), stream)
		if gjson.GetBytes(out, "reasoning.effort").String() != "low" || gjson.GetBytes(out, "max_output_tokens").Int() != 512 || gjson.GetBytes(out, "temperature").Float() != 0.3 || gjson.GetBytes(out, "top_p").Float() != 0.9 || gjson.GetBytes(out, "stream").Bool() != stream {
			t.Fatalf("generation controls changed: %s", out)
		}
		if gjson.GetBytes(out, "context_management").Exists() {
			t.Fatalf("Anthropic-only edits leaked: %s", out)
		}
	}
}

func TestNativeResponsesToClaude(t *testing.T) {
	for _, tc := range []struct{ status, reason, stop string }{
		{"completed", "", "end_turn"}, {"incomplete", "max_output_tokens", "max_tokens"},
	} {
		raw := []byte(`{"object":"response","id":"resp_1","model":"muse-spark-1.3-contributor","status":"` + tc.status + `","incomplete_details":{"reason":"` + tc.reason + `"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":2}}`)
		var param any
		out := ConvertResponsesToClaudeNonStream(context.Background(), "muse-spark-1.3-contributor", nil, nil, raw, &param)
		if gjson.GetBytes(out, "content.0.text").String() != "OK" || gjson.GetBytes(out, "stop_reason").String() != tc.stop || gjson.GetBytes(out, "usage.input_tokens").Int() != 5 {
			t.Fatalf("invalid Claude response: %s", out)
		}
	}
}
