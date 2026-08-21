package executor

import (
	"bytes"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeCursorResponsesUsageNonStream(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24}}`)
	got := normalizeCursorResponsesUsage(sdktranslator.FormatOpenAIResponse, raw)

	if gotValue := gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens"); !gotValue.Exists() || gotValue.Int() != 0 {
		t.Fatalf("usage.input_tokens_details.cached_tokens = %s, want 0", gotValue.Raw)
	}
	if gotValue := gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens"); !gotValue.Exists() || gotValue.Int() != 0 {
		t.Fatalf("usage.output_tokens_details.reasoning_tokens = %s, want 0", gotValue.Raw)
	}
}

func TestNormalizeCursorResponsesUsageStream(t *testing.T) {
	raw := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24}}}`)
	got := normalizeCursorResponsesUsage(sdktranslator.FormatOpenAIResponse, raw)
	jsonBody := bytes.TrimPrefix(got, []byte("data: "))

	if gotValue := gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens"); !gotValue.Exists() || gotValue.Int() != 0 {
		t.Fatalf("response.usage.input_tokens_details.cached_tokens = %s, want 0", gotValue.Raw)
	}
	if gotValue := gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens"); !gotValue.Exists() || gotValue.Int() != 0 {
		t.Fatalf("response.usage.output_tokens_details.reasoning_tokens = %s, want 0", gotValue.Raw)
	}
}

func TestNormalizeCursorResponsesUsageLeavesOtherFormatsUnchanged(t *testing.T) {
	raw := []byte(`{"usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24}}`)
	got := normalizeCursorResponsesUsage(sdktranslator.FormatOpenAI, raw)

	if !bytes.Equal(got, raw) {
		t.Fatalf("non-Responses payload changed: got %s", string(got))
	}
}
