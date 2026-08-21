package executor

import (
	"strings"
	"testing"
	"unicode/utf8"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func TestFindSessionByToolResultsLockedUsesNewestMatchingResult(t *testing.T) {
	t.Parallel()

	older := &cursorSession{
		authID:  "cursor-account",
		pending: []pendingMcpExec{{ToolCallId: "tool-old"}},
	}
	current := &cursorSession{
		authID:  "cursor-account",
		pending: []pendingMcpExec{{ToolCallId: "tool-current"}},
	}
	foreign := &cursorSession{
		authID:  "other-account",
		pending: []pendingMcpExec{{ToolCallId: "tool-current"}},
	}
	executor := &CursorExecutor{sessions: map[string]*cursorSession{
		"cursor-account:older":   older,
		"cursor-account:current": current,
		"other-account:current":  foreign,
	}}

	key, session := executor.findSessionByToolResultsLocked("cursor-account", []toolResultInfo{
		{ToolCallId: "tool-old"},
		{ToolCallId: "tool-current"},
	})
	if key != "cursor-account:current" || session != current {
		t.Fatalf("matched session = %q (%p), want current session %p", key, session, current)
	}
}

func TestFindSessionByToolResultsLockedRejectsUnmatchedOrForeignResults(t *testing.T) {
	t.Parallel()

	executor := &CursorExecutor{sessions: map[string]*cursorSession{
		"other-account:session": {
			authID:  "other-account",
			pending: []pendingMcpExec{{ToolCallId: "tool-foreign"}},
		},
	}}

	for _, results := range [][]toolResultInfo{
		nil,
		{{ToolCallId: ""}},
		{{ToolCallId: "tool-missing"}},
		{{ToolCallId: "tool-foreign"}},
	} {
		if key, session := executor.findSessionByToolResultsLocked("cursor-account", results); key != "" || session != nil {
			t.Fatalf("unexpected session match for %#v: key=%q session=%p", results, key, session)
		}
	}
}

func TestHasPendingSessionForStreamLocked(t *testing.T) {
	t.Parallel()

	wantedStream := &cursorproto.H2Stream{}
	executor := &CursorExecutor{sessions: map[string]*cursorSession{
		"idle": {
			stream: wantedStream,
		},
		"other": {
			stream:  &cursorproto.H2Stream{},
			pending: []pendingMcpExec{{ToolCallId: "tool-other"}},
		},
	}}

	if executor.hasPendingSessionForStreamLocked(wantedStream) {
		t.Fatal("idle stream unexpectedly reported a pending tool call")
	}
	executor.sessions["wanted"] = &cursorSession{
		stream:  wantedStream,
		pending: []pendingMcpExec{{ToolCallId: "tool-wanted"}},
	}
	if !executor.hasPendingSessionForStreamLocked(wantedStream) {
		t.Fatal("pending tool call was not found for stream")
	}
}

func TestFlattenConversationIntoUserTextPreservesToolCallOrder(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[
			{"role":"system","content":"system prompt"},
			{"role":"user","content":"inspect the project"},
			{"role":"assistant","content":"I will read it.","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"main.go\"}"}}]},
			{"role":"tool","tool_call_id":"tool-1","content":"package main"},
			{"role":"user","content":"summarize it"}
		]
	}`)
	parsed := parseOpenAIRequest(payload)
	flattenConversationIntoUserText(parsed)

	wantInOrder := []string{
		"USER: inspect the project",
		"ASSISTANT: I will read it.",
		`ASSISTANT_TOOL_CALL (call_id: tool-1, name: read): {"path":"main.go"}`,
		"TOOL_RESULT (call_id: tool-1): package main",
		"Current request: summarize it",
	}
	position := -1
	for _, want := range wantInOrder {
		next := strings.Index(parsed.UserText, want)
		if next < 0 {
			t.Fatalf("flattened history missing %q:\n%s", want, parsed.UserText)
		}
		if next <= position {
			t.Fatalf("flattened history out of order at %q:\n%s", want, parsed.UserText)
		}
		position = next
	}
	if strings.Count(parsed.UserText, "summarize it") != 1 {
		t.Fatalf("current request duplicated in flattened history:\n%s", parsed.UserText)
	}
	if parsed.Turns != nil || parsed.ToolResults != nil {
		t.Fatalf("structured history was not cleared: turns=%d results=%d", len(parsed.Turns), len(parsed.ToolResults))
	}
}

func TestTruncateCursorHistoryTextPreservesUTF8AtByteLimit(t *testing.T) {
	t.Parallel()

	content := strings.Repeat("a", 7999) + "中" + "tail"
	got := truncateCursorHistoryText(content)
	if !utf8.ValidString(got) {
		t.Fatal("truncated history contains invalid UTF-8")
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 7999)) {
		t.Fatal("truncated history lost valid prefix")
	}
	if strings.Contains(got, "中") {
		t.Fatal("partial boundary rune should be omitted")
	}
	if !strings.HasSuffix(got, "\n... [truncated]") {
		t.Fatal("truncation marker missing")
	}
}

func TestTruncateCursorHistoryTextRepairsInvalidUTF8(t *testing.T) {
	t.Parallel()

	got := truncateCursorHistoryText("before\xffafter")
	if !utf8.ValidString(got) {
		t.Fatal("history contains invalid UTF-8")
	}
	if got != "before\uFFFDafter" {
		t.Fatalf("repaired history = %q, want %q", got, "before\uFFFDafter")
	}
}
