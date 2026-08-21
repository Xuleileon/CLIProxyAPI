package executor

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

func TestSessionMatchesToolResultsRequiresExactPendingID(t *testing.T) {
	t.Parallel()

	session := &cursorSession{pending: []pendingMcpExec{{ToolCallId: "tool-current"}}}
	if sessionMatchesToolResults(session, []toolResultInfo{{ToolCallId: "tool-old"}}) {
		t.Fatal("stale tool result matched the current session")
	}
	if !sessionMatchesToolResults(session, []toolResultInfo{{ToolCallId: "tool-old"}, {ToolCallId: "tool-current"}}) {
		t.Fatal("current tool result did not match the session")
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

func TestPrepareCursorCheckpointContinuationKeepsOnlyPendingDelta(t *testing.T) {
	t.Parallel()

	parsed := &parsedOpenAIRequest{
		UserText: "steer after tool",
		Turns:    []cursorproto.TurnData{{UserText: "old prompt", AssistantText: "old answer"}},
		ToolResults: []toolResultInfo{
			{ToolCallId: "tool-old", Content: "large old history"},
			{ToolCallId: "tool-current", Content: "stale duplicate"},
			{ToolCallId: "tool-current", Content: strings.Repeat("中", 5000)},
		},
	}
	pending := []pendingMcpExec{{ToolCallId: "tool-current", ToolName: "read"}}

	if !prepareCursorCheckpointContinuation(parsed, pending) {
		t.Fatal("checkpoint continuation was not prepared")
	}
	if !utf8.ValidString(parsed.UserText) {
		t.Fatal("checkpoint delta contains invalid UTF-8")
	}
	for _, unwanted := range []string{"large old history", "stale duplicate", "old prompt", "old answer"} {
		if strings.Contains(parsed.UserText, unwanted) {
			t.Fatalf("checkpoint delta retained old history %q", unwanted)
		}
	}
	for _, want := range []string{
		"TOOL_RESULT (call_id: tool-current, name: read)",
		"CURRENT_USER_MESSAGE: steer after tool",
		"Continue from the saved conversation state.",
	} {
		if !strings.Contains(parsed.UserText, want) {
			t.Fatalf("checkpoint delta missing %q: %s", want, parsed.UserText)
		}
	}
	if len(parsed.UserText) > 8300 {
		t.Fatalf("checkpoint delta is too large: %d bytes", len(parsed.UserText))
	}
	if parsed.Turns != nil || parsed.ToolResults != nil {
		t.Fatalf("checkpoint delta retained structured history: turns=%d results=%d", len(parsed.Turns), len(parsed.ToolResults))
	}
}

func TestPrepareCursorCheckpointContinuationRejectsUnmatchedLineage(t *testing.T) {
	t.Parallel()

	parsed := &parsedOpenAIRequest{ToolResults: []toolResultInfo{{ToolCallId: "tool-other", Content: "result"}}}
	if prepareCursorCheckpointContinuation(parsed, []pendingMcpExec{{ToolCallId: "tool-current"}}) {
		t.Fatal("unmatched tool result unexpectedly prepared a checkpoint continuation")
	}
	if len(parsed.ToolResults) != 1 || parsed.UserText != "" {
		t.Fatal("unmatched continuation mutated the parsed request")
	}
}

func TestCursorSourceSessionIDPriority(t *testing.T) {
	t.Parallel()

	body := []byte(`{"session_id":"body-session","metadata":{"user_id":"{\"session_id\":\"claude-body\"}"}}`)
	req := cliproxyexecutor.Request{
		Payload:  body,
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "request-execution"},
	}
	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": {"header-session"}},
		OriginalRequest: body,
		Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "option-execution"},
	}
	if got := cursorSourceSessionID(req, opts); got != "option-execution" {
		t.Fatalf("source session = %q, want option execution session", got)
	}

	delete(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	delete(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	if got := cursorSourceSessionID(req, opts); got != "header-session" {
		t.Fatalf("source session = %q, want Claude Code header", got)
	}

	opts.Headers = nil
	if got := cursorSourceSessionID(req, opts); got != "claude-body" {
		t.Fatalf("source session = %q, want Claude metadata session", got)
	}
}

func TestCursorSourceSessionIDUsesDerivedFallback(t *testing.T) {
	t.Parallel()

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-root",
	}}
	if got := cursorSourceSessionID(cliproxyexecutor.Request{}, opts); got != "ctx:v1:derived-root" {
		t.Fatalf("source session = %q, want derived fallback", got)
	}
}

func TestScopeCursorSourceSessionIDSeparatesConversationRoots(t *testing.T) {
	t.Parallel()

	first := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"system","content":"rules"},{"role":"user","content":"task one"}]}`))
	continued := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"system","content":"rules"},{"role":"user","content":"task one"},{"role":"assistant","content":"working"},{"role":"tool","tool_call_id":"tool-1","content":"done"}]}`))
	second := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"system","content":"rules"},{"role":"user","content":"task two"}]}`))

	firstID := scopeCursorSourceSessionID("claude-session", first)
	if continuedID := scopeCursorSourceSessionID("claude-session", continued); continuedID != firstID {
		t.Fatalf("continued conversation scope changed: %q != %q", continuedID, firstID)
	}
	if secondID := scopeCursorSourceSessionID("claude-session", second); secondID == firstID {
		t.Fatal("different subagent roots shared a conversation scope")
	}
}

func TestScopeCursorSourceSessionIDHashesFullConversationRoot(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("same-prefix-", 100)
	first := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"user","content":"` + prefix + `one"}]}`))
	second := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"user","content":"` + prefix + `two"}]}`))
	if scopeCursorSourceSessionID("shared-session", first) == scopeCursorSourceSessionID("shared-session", second) {
		t.Fatal("conversation roots that differ after a long common prefix collided")
	}
}

func TestCloneSavedCursorCheckpointIsDeepCopy(t *testing.T) {
	t.Parallel()

	original := &savedCheckpoint{data: []byte("checkpoint"), blobStore: map[string][]byte{"blob": []byte("value")}, authID: "auth"}
	cloned, ok := cloneSavedCursorCheckpoint(original)
	if !ok {
		t.Fatal("checkpoint clone failed")
	}
	cloned.data[0] = 'C'
	cloned.blobStore["blob"][0] = 'V'
	if string(original.data) != "checkpoint" || string(original.blobStore["blob"]) != "value" {
		t.Fatal("checkpoint clone aliases mutable data")
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
