package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildCursorOpenAIChatCompletionSplitsReasoning(t *testing.T) {
	raw := buildCursorOpenAIChatCompletion(
		"chatcmpl-test",
		123,
		"composer-2.5",
		"4",
		"User asked for 2+2. Answer is 4.",
		10,
		3,
	)

	if got := gjson.GetBytes(raw, "choices.0.message.content").String(); got != "4" {
		t.Fatalf("content = %q, want %q; raw=%s", got, "4", raw)
	}
	if got := gjson.GetBytes(raw, "choices.0.message.reasoning_content").String(); got != "User asked for 2+2. Answer is 4." {
		t.Fatalf("reasoning_content = %q, want reasoning text; raw=%s", got, raw)
	}
	if got := gjson.GetBytes(raw, "choices.0.message.role").String(); got != "assistant" {
		t.Fatalf("role = %q, want assistant", got)
	}
	if got := gjson.GetBytes(raw, "usage.prompt_tokens").Int(); got != 10 {
		t.Fatalf("prompt_tokens = %d, want 10", got)
	}
	if got := gjson.GetBytes(raw, "usage.completion_tokens").Int(); got != 3 {
		t.Fatalf("completion_tokens = %d, want 3", got)
	}
	if got := gjson.GetBytes(raw, "usage.total_tokens").Int(); got != 13 {
		t.Fatalf("total_tokens = %d, want 13", got)
	}
}

func TestBuildCursorOpenAIChatCompletionOmitsEmptyReasoning(t *testing.T) {
	raw := buildCursorOpenAIChatCompletion("id", 1, "composer-2.5", "hello", "", 0, 0)
	if gjson.GetBytes(raw, "choices.0.message.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content omitted when empty; raw=%s", raw)
	}
	if got := gjson.GetBytes(raw, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
}

func TestBuildCursorOpenAIChatCompletionReasoningOnly(t *testing.T) {
	raw := buildCursorOpenAIChatCompletion("id", 1, "composer-2.5", "", "only thinking", 0, 0)
	if got := gjson.GetBytes(raw, "choices.0.message.content").String(); got != "" {
		t.Fatalf("content = %q, want empty", got)
	}
	if got := gjson.GetBytes(raw, "choices.0.message.reasoning_content").String(); got != "only thinking" {
		t.Fatalf("reasoning_content = %q, want only thinking", got)
	}
}
