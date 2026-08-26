package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	claudetoopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestCursorCheckpointSurvivesExecutorRestart(t *testing.T) {
	t.Parallel()
	authDir := t.TempDir()
	conversationID := "conversation-restart"
	authID := "cursor-account"

	first := NewCursorExecutor(&config.Config{AuthDir: authDir})
	generation := first.beginCursorCheckpointGeneration(conversationID, authID)
	pending := []pendingMcpExec{{ToolCallId: "tool-1", ToolName: "read", Args: `{"path":"secret"}`}}
	first.saveCursorCheckpoint(conversationID, authID, generation, []byte("checkpoint"), map[string][]byte{"blob": []byte("image")}, pending)

	second := NewCursorExecutor(&config.Config{AuthDir: authDir})
	loaded, ok := second.loadCursorCheckpoint(conversationID, authID)
	if !ok || string(loaded.data) != "checkpoint" || string(loaded.blobStore["blob"]) != "image" {
		t.Fatalf("restored checkpoint = ok %t, value %#v", ok, loaded)
	}
	if len(loaded.pending) != 1 || loaded.pending[0].ToolCallId != "tool-1" || loaded.pending[0].ToolName != "read" || loaded.pending[0].Args != "" {
		t.Fatalf("restored checkpoint lineage = %#v", loaded.pending)
	}
}

func TestCursorCheckpointGenerationRejectsLateFramesAndKeepsLateBlobs(t *testing.T) {
	t.Parallel()
	executor := NewCursorExecutor(&config.Config{AuthDir: t.TempDir()})
	conversationID := "conversation-generation"
	authID := "cursor-account"
	oldGeneration := executor.beginCursorCheckpointGeneration(conversationID, authID)
	executor.saveCursorCheckpoint(conversationID, authID, oldGeneration, []byte("old"), nil, nil)

	currentGeneration := executor.beginCursorCheckpointGeneration(conversationID, authID)
	executor.saveCursorCheckpoint(conversationID, authID, currentGeneration, []byte("current"), nil, []pendingMcpExec{{ToolCallId: "tool-current", ToolName: "read"}})
	executor.saveCursorCheckpoint(conversationID, authID, oldGeneration, []byte("late-old"), map[string][]byte{"wrong": []byte("wrong")}, nil)
	executor.updateCursorCheckpointBlob(conversationID, authID, oldGeneration, "wrong", []byte("wrong"))
	executor.updateCursorCheckpointBlob(conversationID, authID, currentGeneration, "late", []byte("image"))

	loaded, ok := executor.loadCursorCheckpoint(conversationID, authID)
	if !ok || string(loaded.data) != "current" {
		t.Fatalf("current checkpoint = ok %t, value %#v", ok, loaded)
	}
	if _, exists := loaded.blobStore["wrong"]; exists {
		t.Fatalf("superseded generation mutated checkpoint: %#v", loaded.blobStore)
	}
	if string(loaded.blobStore["late"]) != "image" {
		t.Fatalf("late checkpoint blob was not retained: %#v", loaded.blobStore)
	}

	restarted := NewCursorExecutor(&config.Config{AuthDir: executor.cfg.AuthDir})
	persisted, ok := restarted.loadCursorCheckpoint(conversationID, authID)
	if !ok || string(persisted.data) != "current" || string(persisted.blobStore["late"]) != "image" {
		t.Fatalf("persisted generation = ok %t, value %#v", ok, persisted)
	}
}

func TestCursorCheckpointMemoryIsolatedByAuth(t *testing.T) {
	t.Parallel()
	executor := NewCursorExecutor(&config.Config{AuthDir: t.TempDir()})
	conversationID := "shared-conversation"
	firstGeneration := executor.beginCursorCheckpointGeneration(conversationID, "account-a")
	secondGeneration := executor.beginCursorCheckpointGeneration(conversationID, "account-b")
	executor.saveCursorCheckpoint(conversationID, "account-a", firstGeneration, []byte("checkpoint-a"), nil, nil)
	executor.saveCursorCheckpoint(conversationID, "account-b", secondGeneration, []byte("checkpoint-b"), nil, nil)

	first, firstOK := executor.loadCursorCheckpoint(conversationID, "account-a")
	second, secondOK := executor.loadCursorCheckpoint(conversationID, "account-b")
	if !firstOK || !secondOK || string(first.data) != "checkpoint-a" || string(second.data) != "checkpoint-b" {
		t.Fatalf("auth-scoped checkpoints = first(%t, %q) second(%t, %q)", firstOK, first.data, secondOK, second.data)
	}
}

func TestCursorCheckpointPendingStateClearsOnlyOnNewCheckpoint(t *testing.T) {
	t.Parallel()
	executor := NewCursorExecutor(&config.Config{AuthDir: t.TempDir()})
	conversationID := "conversation-pending"
	authID := "cursor-account"
	generation := executor.beginCursorCheckpointGeneration(conversationID, authID)
	executor.saveCursorCheckpoint(conversationID, authID, generation, []byte("before-result"), nil, []pendingMcpExec{{ToolCallId: "tool-1", ToolName: "read"}})
	executor.updateCursorCheckpointPending(conversationID, authID, generation, nil)

	pending, ok := executor.loadCursorCheckpoint(conversationID, authID)
	if !ok || len(pending.pending) != 1 {
		t.Fatalf("older checkpoint lost pending marker: ok=%t checkpoint=%#v", ok, pending)
	}
	executor.saveCursorCheckpoint(conversationID, authID, generation, []byte("after-result"), nil, nil)
	completed, ok := executor.loadCursorCheckpoint(conversationID, authID)
	if !ok || len(completed.pending) != 0 || string(completed.data) != "after-result" {
		t.Fatalf("new checkpoint did not clear pending marker: ok=%t checkpoint=%#v", ok, completed)
	}
}

func TestCursorCheckpointColdReplayDecision(t *testing.T) {
	t.Parallel()
	completed := &savedCheckpoint{data: []byte("checkpoint")}
	pending := &savedCheckpoint{data: []byte("checkpoint"), pending: []pendingMcpExec{{ToolCallId: "tool-1"}}}
	if cursorCheckpointNeedsColdToolReplay(completed, false, 132) {
		t.Fatal("completed checkpoint rejected only because request contains historical tool results")
	}
	if !cursorCheckpointNeedsColdToolReplay(pending, false, 0) {
		t.Fatal("checkpoint waiting for a tool result was considered restart-safe")
	}
	if !cursorCheckpointNeedsColdToolReplay(completed, true, 1) {
		t.Fatal("failed live continuation was considered restart-safe")
	}
}

func TestCursorVisibleInputEstimateExcludesImagesBlobsAndCheckpoint(t *testing.T) {
	t.Parallel()
	largeBinary := bytes.Repeat([]byte{0xff}, 2<<20)
	params := &cursorproto.RunRequestParams{
		SystemPrompt:  "system",
		UserText:      "current",
		Turns:         []cursorproto.TurnData{{UserText: "old-user", AssistantText: "old-assistant"}},
		McpTools:      []cursorproto.McpToolDef{{Name: "read", Description: "file", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Images:        []cursorproto.ImageData{{MimeType: "image/png", Data: largeBinary}},
		RawCheckpoint: largeBinary,
		BlobStore:     map[string][]byte{"blob": largeBinary},
	}
	// The checkpoint already represents system and historical turns; only the
	// new text delta and tool declarations are sent alongside it.
	want := len("current") + len("read") + len("file") + len(`{"type":"object"}`)
	if got := cursorVisibleInputBytes(params); got != want {
		t.Fatalf("visible input bytes = %d, want %d", got, want)
	}
	params.RawCheckpoint = nil
	want += len("system") + len("old-user") + len("old-assistant")
	if got := cursorVisibleInputBytes(params); got != want {
		t.Fatalf("cold visible input bytes = %d, want %d", got, want)
	}
}

func TestResumeWithToolResultsReusesExistingOutput(t *testing.T) {
	t.Parallel()

	toolResultCh := make(chan []toolResultInfo, 1)
	resumeOutCh := make(chan cliproxyexecutor.StreamChunk, 1)
	switched := false
	session := &cursorSession{
		toolResultCh: toolResultCh,
		resumeOutCh:  resumeOutCh,
		switchOutput: func(ch chan cliproxyexecutor.StreamChunk) {
			if ch != resumeOutCh {
				t.Fatalf("switched to unexpected output channel")
			}
			switched = true
		},
	}

	executor := &CursorExecutor{}
	result, err := executor.resumeWithToolResults(context.Background(), session, []toolResultInfo{{ToolCallId: "tool-current", Content: "result"}})
	if err != nil {
		t.Fatalf("resumeWithToolResults error: %v", err)
	}
	if !switched || result.Chunks != resumeOutCh {
		t.Fatal("existing Run output was not reused")
	}
	results := <-toolResultCh
	if len(results) != 1 || results[0].ToolCallId != "tool-current" {
		t.Fatalf("forwarded tool results = %#v", results)
	}
}

func TestAdaptCursorTeammateReplyRequiresSendMessage(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[{"role":"user","content":"<teammate-message teammate_id=\"team-lead\" summary=\"send findings\">Please report to main.</teammate-message>"}],
		"tools":[{"type":"function","function":{"name":"SendMessage","description":"Send to teammate","parameters":{"type":"object"}}}]
	}`)
	parsed := parseOpenAIRequest(payload)
	adaptCursorTeammateReply(parsed)
	for _, want := range []string{"plain assistant response remains only", "calling the SendMessage tool exactly once"} {
		if !strings.Contains(parsed.UserText, want) {
			t.Fatalf("adapted teammate message missing %q: %s", want, parsed.UserText)
		}
	}

	idle := &parsedOpenAIRequest{
		UserText: `<teammate-message teammate_id="worker">{"type":"idle_notification"}</teammate-message>`,
		Tools:    parsed.Tools,
	}
	adaptCursorTeammateReply(idle)
	for _, want := range []string{"only a lifecycle notification", "not a TaskOutput task_id", "Never call TaskOutput"} {
		if !strings.Contains(idle.UserText, want) {
			t.Fatalf("adapted idle notification missing %q: %s", want, idle.UserText)
		}
	}
}

func TestAdaptCursorTeammateReplyLoadsDeferredSendMessage(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[{"role":"user","content":"<teammate-message teammate_id=\"team-lead\" summary=\"send findings\">Please report to main.</teammate-message>"}],
		"tools":[{"type":"function","function":{"name":"ToolSearch","description":"Load deferred tools","parameters":{"type":"object"}}}]
	}`)
	parsed := parseOpenAIRequest(payload)
	adaptCursorTeammateReply(parsed)
	for _, want := range []string{"SendMessage is deferred", "First call ToolSearch", "then call SendMessage exactly once"} {
		if !strings.Contains(parsed.UserText, want) {
			t.Fatalf("adapted deferred teammate message missing %q: %s", want, parsed.UserText)
		}
	}
}

func TestRejectCursorTeammateTaskOutput(t *testing.T) {
	t.Parallel()

	if got := rejectCursorTeammateTaskOutput("TaskOutput", `{"task_id":"project-scan-v3@session-1facdb6e"}`); !strings.Contains(got, "teammate Agent ID") {
		t.Fatalf("rejection = %q, want teammate ID guidance", got)
	}
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "TaskOutput", args: `{"task_id":"task-123"}`},
		{name: "SendMessage", args: `{"task_id":"project-scan-v3@session-1facdb6e"}`},
		{name: "TaskOutput", args: `{}`},
	} {
		if got := rejectCursorTeammateTaskOutput(tc.name, tc.args); got != "" {
			t.Fatalf("rejectCursorTeammateTaskOutput(%q, %s) = %q, want no rejection", tc.name, tc.args, got)
		}
	}
}

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
		"cursor-account:resuming": {
			authID:   "cursor-account",
			pending:  []pendingMcpExec{{ToolCallId: "tool-busy"}},
			resuming: true,
		},
	}}

	for _, results := range [][]toolResultInfo{
		nil,
		{{ToolCallId: ""}},
		{{ToolCallId: "tool-missing"}},
		{{ToolCallId: "tool-foreign"}},
		{{ToolCallId: "tool-busy"}},
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

func TestSessionMatchesToolResultsRequiresCompletePendingBatch(t *testing.T) {
	t.Parallel()

	session := &cursorSession{pending: []pendingMcpExec{
		{ToolCallId: "tool-a"},
		{ToolCallId: "tool-b"},
	}}
	if sessionMatchesToolResults(session, []toolResultInfo{{ToolCallId: "tool-a"}}) {
		t.Fatal("partial tool result batch matched the active session")
	}
	if !sessionMatchesToolResults(session, []toolResultInfo{
		{ToolCallId: "tool-old"},
		{ToolCallId: "tool-b"},
		{ToolCallId: "tool-a"},
	}) {
		t.Fatal("complete out-of-order tool result batch did not match")
	}

	executor := &CursorExecutor{sessions: map[string]*cursorSession{"cursor-account:active": session}}
	session.authID = "cursor-account"
	if key, matched := executor.findSessionByToolResultsLocked("cursor-account", []toolResultInfo{{ToolCallId: "tool-a"}}); key != "" || matched != nil {
		t.Fatalf("partial batch matched session: key=%q session=%p", key, matched)
	}
	key, matched, pending := executor.findPartialSessionByToolResultsLocked("cursor-account", []toolResultInfo{{ToolCallId: "tool-a"}})
	if key != "cursor-account:active" || matched != 1 || pending != 2 {
		t.Fatalf("partial batch classification = key=%q matched=%d pending=%d", key, matched, pending)
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
		"TOOL_RESULT (call_id: tool-1, status: success): package main",
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

func TestFlattenConversationIntoUserTextPreservesToolErrorsAndUnsupportedContent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"tool-error","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"tool-error","content":[
				{"type":"text","text":"failed"},
				{"type":"image_url","image_url":{"url":"https://example.com/private.png?token=secret"}},
				{"type":"resource","uri":"file:///report.json"}
			]},
			{"role":"user","content":"continue"}
		]
	}`)
	parsed := parseOpenAIRequest(payload)
	parsed.ToolResults[0].IsError = true
	flattenConversationIntoUserText(parsed)

	for _, want := range []string{
		"TOOL_RESULT (call_id: tool-error, status: error):",
		"remote image not transferred",
		"host=example.com",
		"unsupported content block type=resource",
	} {
		if !strings.Contains(parsed.UserText, want) {
			t.Fatalf("flattened history missing %q: %s", want, parsed.UserText)
		}
	}
	if strings.Contains(parsed.UserText, "token=secret") {
		t.Fatalf("remote image diagnostic leaked query data: %s", parsed.UserText)
	}
}

func TestParseDataURLSupportsParametersAndRejectsNonImages(t *testing.T) {
	t.Parallel()

	image := parseDataURL("data:image/svg+xml;charset=utf-8;base64,PHN2Zy8+")
	if image == nil || image.MimeType != "image/svg+xml" || string(image.Data) != "<svg/>" {
		t.Fatalf("parameterized image data URL = %#v", image)
	}
	if got := parseDataURL("data:text/plain;base64,aGVsbG8="); got != nil {
		t.Fatalf("non-image data URL was accepted: %#v", got)
	}
}

func TestBuildRunRequestParamsHonorsToolChoice(t *testing.T) {
	t.Parallel()

	tool := `{"type":"function","function":{"name":"read","description":"read","parameters":{"type":"object"}}}`
	tests := []struct {
		name      string
		choice    string
		wantMode  cursorproto.AgentMode
		wantTools int
		wantErr   bool
	}{
		{name: "none", choice: `"none"`, wantMode: cursorproto.AgentModeAsk},
		{name: "auto", choice: `"auto"`, wantMode: cursorproto.AgentModeAgent, wantTools: 1},
		{name: "required", choice: `"required"`, wantErr: true},
		{name: "specific", choice: `{"type":"function","function":{"name":"read"}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := parseOpenAIRequest([]byte(`{"model":"composer-2.5-fast","messages":[{"role":"user","content":"hi"}],"tools":[` + tool + `],"tool_choice":` + tt.choice + `}`))
			params, err := buildRunRequestParams(parsed, "conv", "composer-2.5-fast")
			if (err != nil) != tt.wantErr {
				t.Fatalf("build error = %v, wantErr=%t", err, tt.wantErr)
			}
			if tt.wantErr {
				statusErr, ok := err.(interface{ StatusCode() int })
				if !ok || statusErr.StatusCode() != http.StatusBadRequest {
					t.Fatalf("tool choice error status = %v, want 400", err)
				}
				return
			}
			if params.AgentMode != tt.wantMode || len(params.McpTools) != tt.wantTools {
				t.Fatalf("mode=%v tools=%d, want mode=%v tools=%d", params.AgentMode, len(params.McpTools), tt.wantMode, tt.wantTools)
			}
		})
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

func TestBridgeCursorNativeExecMapsClaudeCodeTools(t *testing.T) {
	t.Parallel()

	tools := []cursorproto.McpToolDef{
		{Name: "Bash", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"}},"required":["command"]}`)},
		{Name: "Glob", InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`)},
		{Name: "Grep", InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"output_mode":{"type":"string"}},"required":["pattern"]}`)},
		{Name: "Read", InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`)},
	}

	shell, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{
		Type: cursorproto.ServerMsgExecShellStream, ExecMsgId: 1, ExecId: "exec-shell", ToolCallId: "tool-shell", Command: "pwd", Timeout: 120000,
	}, tools)
	if !ok || shell.Kind != cursorExecShellStream || shell.ToolName != "Bash" || shell.ToolCallId != "tool-shell" {
		t.Fatalf("shell bridge = %#v, ok=%t", shell, ok)
	}
	var shellArgs map[string]any
	if err := json.Unmarshal([]byte(shell.Args), &shellArgs); err != nil {
		t.Fatalf("decode shell args: %v", err)
	}
	if shellArgs["command"] != "pwd" || shellArgs["timeout"] != float64(120000) {
		t.Fatalf("shell args = %#v", shellArgs)
	}

	glob, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{
		Type: cursorproto.ServerMsgExecGrepArgs, ExecMsgId: 2, ToolCallId: "tool-glob", Glob: "**/README*", OutputMode: "files_with_matches",
	}, tools)
	if !ok || glob.Kind != cursorExecGrep || glob.ToolName != "Glob" {
		t.Fatalf("grep-as-glob bridge = %#v, ok=%t", glob, ok)
	}
	var globArgs map[string]any
	if err := json.Unmarshal([]byte(glob.Args), &globArgs); err != nil {
		t.Fatalf("decode glob args: %v", err)
	}
	if globArgs["pattern"] != "**/README*" {
		t.Fatalf("glob args = %#v", globArgs)
	}

	grep, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{
		Type: cursorproto.ServerMsgExecGrepArgs, ExecMsgId: 3, Pattern: "TODO", Path: "internal", Glob: "*.go", OutputMode: "content",
	}, tools)
	if !ok || grep.Kind != cursorExecGrep || grep.ToolName != "Grep" {
		t.Fatalf("grep bridge = %#v, ok=%t", grep, ok)
	}

	read, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{
		Type: cursorproto.ServerMsgExecReadArgs, ExecMsgId: 4, Path: "README.md",
	}, tools)
	if !ok || read.Kind != cursorExecRead || !strings.Contains(read.Args, `"file_path":"README.md"`) {
		t.Fatalf("read bridge = %#v, ok=%t", read, ok)
	}

	prefixed := []cursorproto.McpToolDef{{
		Name:        "acp_Read",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
	}}
	prefixedRead, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{
		Type: cursorproto.ServerMsgExecReadArgs, ExecMsgId: 5, Path: "AGENTS.md",
	}, prefixed)
	if !ok || prefixedRead.ToolName != "Read" {
		t.Fatalf("prefixed read bridge = %#v, ok=%t", prefixedRead, ok)
	}
}

func TestBridgeCursorNativeExecRejectsUnrepresentableTools(t *testing.T) {
	t.Parallel()

	if _, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{Type: cursorproto.ServerMsgExecDeleteArgs, Path: "file.txt"}, nil); ok {
		t.Fatal("delete unexpectedly bridged without a declared Delete tool")
	}
	if _, ok := bridgeCursorNativeExec(&cursorproto.DecodedServerMessage{Type: cursorproto.ServerMsgExecWriteArgs, Path: "file.bin", FileBytes: []byte{0xff}}, []cursorproto.McpToolDef{{Name: "Write"}}); ok {
		t.Fatal("binary write unexpectedly bridged through a text-only Write tool")
	}
}

func TestEncodeCursorExecCompletionAlwaysClosesExec(t *testing.T) {
	t.Parallel()

	for _, pending := range []pendingMcpExec{
		{ExecMsgId: 11, ExecId: "mcp", Kind: cursorExecMCP},
		{ExecMsgId: 12, ExecId: "shell", Kind: cursorExecShellStream},
		{ExecMsgId: 13, ExecId: "grep", Kind: cursorExecGrep, OutputMode: "files_with_matches"},
	} {
		messages := encodeCursorExecCompletion(pending, toolResultInfo{Content: "result"})
		if len(messages) < 2 {
			t.Fatalf("completion kind %d emitted %d messages", pending.Kind, len(messages))
		}
		wantClose := cursorproto.EncodeExecStreamClose(pending.ExecMsgId)
		if !bytes.Equal(messages[len(messages)-1], wantClose) {
			t.Fatalf("completion kind %d did not end with stream close", pending.Kind)
		}
	}
}

type fakeCursorToolResultStream struct {
	dataCh chan []byte
	doneCh chan struct{}
	writes [][]byte
}

func (stream *fakeCursorToolResultStream) Data() <-chan []byte   { return stream.dataCh }
func (stream *fakeCursorToolResultStream) Done() <-chan struct{} { return stream.doneCh }
func (stream *fakeCursorToolResultStream) Err() error            { return nil }
func (stream *fakeCursorToolResultStream) Write(data []byte) error {
	stream.writes = append(stream.writes, append([]byte(nil), data...))
	return nil
}

func TestWaitForCursorToolResultsQueuesLateParallelExecBatch(t *testing.T) {
	t.Parallel()

	stream := &fakeCursorToolResultStream{
		dataCh: make(chan []byte),
		doneCh: make(chan struct{}),
	}
	toolResultCh := make(chan []toolResultInfo)
	tools := []cursorproto.McpToolDef{{
		Name:        "Read",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
	}}

	type waitResult struct {
		results []toolResultInfo
		queued  []pendingMcpExec
		err     error
	}
	outcomeCh := make(chan waitResult, 1)
	go func() {
		results, queued, err := waitForCursorToolResults(
			context.Background(), stream, &bytes.Buffer{}, map[string][]byte{}, tools,
			toolResultCh, nil, nil, nil,
		)
		outcomeCh <- waitResult{results: results, queued: queued, err: err}
	}()

	var lateBatchFrames []byte
	for index := 0; index < 8; index++ {
		lateBatchFrames = append(lateBatchFrames, testCursorReadServerFrame(
			uint32(index+20),
			"exec-late-"+string(rune('a'+index)),
			"tool-late-"+string(rune('a'+index)),
		)...)
	}
	stream.dataCh <- lateBatchFrames
	wantResults := []toolResultInfo{{ToolCallId: "tool-current", Content: "done"}}
	toolResultCh <- wantResults

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("waitForCursorToolResults() error = %v", outcome.err)
	}
	if len(outcome.results) != 1 || outcome.results[0].ToolCallId != "tool-current" {
		t.Fatalf("returned results = %#v", outcome.results)
	}
	if len(outcome.queued) != 8 {
		t.Fatalf("queued batch size = %d, want 8", len(outcome.queued))
	}
	for index, pending := range outcome.queued {
		wantID := "tool-late-" + string(rune('a'+index))
		if pending.ToolCallId != wantID || pending.ToolName != "Read" || pending.ExecMsgId != uint32(index+20) {
			t.Fatalf("queued[%d] = %#v, want tool call %q", index, pending, wantID)
		}
	}
	if len(stream.writes) != 0 {
		t.Fatalf("late bridged calls emitted %d upstream responses before client execution", len(stream.writes))
	}
}

func TestWaitForCursorToolResultsQueuesLateAgentBatch(t *testing.T) {
	t.Parallel()

	stream := &fakeCursorToolResultStream{
		dataCh: make(chan []byte),
		doneCh: make(chan struct{}),
	}
	toolResultCh := make(chan []toolResultInfo)

	type waitResult struct {
		queued []pendingMcpExec
		err    error
	}
	outcomeCh := make(chan waitResult, 1)
	go func() {
		_, queued, err := waitForCursorToolResults(
			context.Background(), stream, &bytes.Buffer{}, map[string][]byte{}, nil,
			toolResultCh, nil, nil, nil,
		)
		outcomeCh <- waitResult{queued: queued, err: err}
	}()

	var lateBatchFrames []byte
	for index := 0; index < 8; index++ {
		lateBatchFrames = append(lateBatchFrames, testCursorMCPServerFrame(
			uint32(index+40),
			"exec-agent-"+string(rune('a'+index)),
			"tool-agent-"+string(rune('a'+index)),
			"Agent",
		)...)
	}
	stream.dataCh <- lateBatchFrames
	toolResultCh <- []toolResultInfo{{ToolCallId: "tool-current", Content: "done"}}

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("waitForCursorToolResults() error = %v", outcome.err)
	}
	if len(outcome.queued) != 8 {
		t.Fatalf("queued agent batch size = %d, want 8", len(outcome.queued))
	}
	for index, pending := range outcome.queued {
		wantID := "tool-agent-" + string(rune('a'+index))
		if pending.ToolCallId != wantID || pending.ToolName != "Agent" || pending.Kind != cursorExecMCP {
			t.Fatalf("queued agent[%d] = %#v, want tool call %q", index, pending, wantID)
		}
	}
	if len(stream.writes) != 0 {
		t.Fatalf("late agent calls emitted %d upstream responses before client execution", len(stream.writes))
	}
}

func TestWaitForCursorToolResultsQueuesFragmentedLateAgentCall(t *testing.T) {
	t.Parallel()

	stream := &fakeCursorToolResultStream{
		dataCh: make(chan []byte),
		doneCh: make(chan struct{}),
	}
	toolResultCh := make(chan []toolResultInfo)

	type waitResult struct {
		queued []pendingMcpExec
		err    error
	}
	outcomeCh := make(chan waitResult, 1)
	go func() {
		_, queued, err := waitForCursorToolResults(
			context.Background(), stream, &bytes.Buffer{}, map[string][]byte{}, nil,
			toolResultCh, nil, nil, nil,
		)
		outcomeCh <- waitResult{queued: queued, err: err}
	}()

	frame := testCursorMCPServerFrame(60, "exec-agent-fragmented", "tool-agent-fragmented", "Agent")
	split := len(frame) / 2
	stream.dataCh <- frame[:split]
	stream.dataCh <- frame[split:]
	toolResultCh <- []toolResultInfo{{ToolCallId: "tool-current", Content: "done"}}

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("waitForCursorToolResults() error = %v", outcome.err)
	}
	if len(outcome.queued) != 1 || outcome.queued[0].ToolCallId != "tool-agent-fragmented" {
		t.Fatalf("fragmented queued batch = %#v", outcome.queued)
	}
}

func TestWaitForCursorToolResultsAcknowledgesPreCompact(t *testing.T) {
	t.Parallel()
	stream := &fakeCursorToolResultStream{dataCh: make(chan []byte), doneCh: make(chan struct{})}
	toolResultCh := make(chan []toolResultInfo)
	outcomeCh := make(chan error, 1)
	go func() {
		_, _, err := waitForCursorToolResults(
			context.Background(), stream, &bytes.Buffer{}, map[string][]byte{}, nil,
			toolResultCh, nil, nil, nil,
		)
		outcomeCh <- err
	}()

	stream.dataCh <- testCursorPreCompactServerFrame(91, "exec-pre-compact")
	toolResultCh <- []toolResultInfo{{ToolCallId: "tool-current", Content: "done"}}
	if err := <-outcomeCh; err != nil {
		t.Fatalf("waitForCursorToolResults() error = %v", err)
	}
	if len(stream.writes) != 2 {
		t.Fatalf("pre-compact writes = %d, want result + close", len(stream.writes))
	}
	wantResult := cursorproto.FrameConnectMessage(cursorproto.EncodeExecPreCompactResult(91, "exec-pre-compact", ""), 0)
	wantClose := cursorproto.FrameConnectMessage(cursorproto.EncodeExecStreamClose(91), 0)
	if !bytes.Equal(stream.writes[0], wantResult) || !bytes.Equal(stream.writes[1], wantClose) {
		t.Fatalf("unexpected pre-compact acknowledgement: %#v", stream.writes)
	}
}

func testCursorReadServerFrame(messageID uint32, execID, toolCallID string) []byte {
	var args []byte
	args = appendCursorTestString(args, cursorproto.RA_Path, "README.md")
	args = appendCursorTestString(args, cursorproto.RA_ToolCallID, toolCallID)

	var exec []byte
	exec = appendCursorTestVarint(exec, cursorproto.ESM_Id, uint64(messageID))
	exec = appendCursorTestBytes(exec, cursorproto.ESM_ReadArgs, args)
	exec = appendCursorTestString(exec, cursorproto.ESM_ExecId, execID)

	serverMessage := appendCursorTestBytes(nil, cursorproto.ASM_ExecServerMessage, exec)
	return cursorproto.FrameConnectMessage(serverMessage, 0)
}

func testCursorMCPServerFrame(messageID uint32, execID, toolCallID, toolName string) []byte {
	var args []byte
	args = appendCursorTestString(args, cursorproto.MCA_Name, toolName)
	args = appendCursorTestString(args, cursorproto.MCA_ToolCallId, toolCallID)

	var exec []byte
	exec = appendCursorTestVarint(exec, cursorproto.ESM_Id, uint64(messageID))
	exec = appendCursorTestBytes(exec, cursorproto.ESM_McpArgs, args)
	exec = appendCursorTestString(exec, cursorproto.ESM_ExecId, execID)

	serverMessage := appendCursorTestBytes(nil, cursorproto.ASM_ExecServerMessage, exec)
	return cursorproto.FrameConnectMessage(serverMessage, 0)
}

func testCursorPreCompactServerFrame(messageID uint32, execID string) []byte {
	preCompact := appendCursorTestString(nil, 1, "automatic")
	hookArgs := appendCursorTestBytes(nil, 1, preCompact)
	var exec []byte
	exec = appendCursorTestVarint(exec, cursorproto.ESM_Id, uint64(messageID))
	exec = appendCursorTestBytes(exec, cursorproto.ESM_ExecuteHookArgs, hookArgs)
	exec = appendCursorTestString(exec, cursorproto.ESM_ExecId, execID)
	serverMessage := appendCursorTestBytes(nil, cursorproto.ASM_ExecServerMessage, exec)
	return cursorproto.FrameConnectMessage(serverMessage, 0)
}

func appendCursorTestString(dst []byte, number protowire.Number, value string) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendString(dst, value)
}

func appendCursorTestBytes(dst []byte, number protowire.Number, value []byte) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendBytes(dst, value)
}

func appendCursorTestVarint(dst []byte, number protowire.Number, value uint64) []byte {
	dst = protowire.AppendTag(dst, number, protowire.VarintType)
	return protowire.AppendVarint(dst, value)
}

func TestApplyOriginalToolResultErrorsPreservesClaudeErrorFlag(t *testing.T) {
	t.Parallel()

	parsed := &parsedOpenAIRequest{ToolResults: []toolResultInfo{{ToolCallId: "tool-ok"}, {ToolCallId: "tool-failed"}}}
	original := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-ok","content":"ok"},{"type":"tool_result","tool_use_id":"tool-failed","content":[{"type":"json","json":{"reason":"denied"}}],"is_error":true}]}]}`)
	applyOriginalToolResultErrors(parsed, original)
	if parsed.ToolResults[0].IsError || !parsed.ToolResults[1].IsError {
		t.Fatalf("tool result error flags = %#v", parsed.ToolResults)
	}
	if got := string(parsed.ToolResults[1].StructuredContent); got != `{"reason":"denied"}` {
		t.Fatalf("structured tool result = %s", got)
	}
}

func TestParseOpenAIRequestPreservesToolResultImages(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"tool-image","type":"function","function":{"name":"read","arguments":"{\"path\":\"shot.png\"}"}}]},
			{"role":"tool","tool_call_id":"tool-image","content":[
				{"type":"text","text":"Read image file [image/png]"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AQIDBA=="}}
			]}
		]
	}`)

	parsed := parseOpenAIRequest(payload)
	if len(parsed.ToolResults) != 1 {
		t.Fatalf("tool results = %d, want 1", len(parsed.ToolResults))
	}
	result := parsed.ToolResults[0]
	if result.Content != "Read image file [image/png]" {
		t.Fatalf("tool result text = %q", result.Content)
	}
	if len(result.Images) != 1 {
		t.Fatalf("tool result images = %d, want 1", len(result.Images))
	}
	if result.Images[0].MimeType != "image/png" || !bytes.Equal(result.Images[0].Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("tool result image = %#v", result.Images[0])
	}
}

func TestFlattenConversationIntoUserTextPreservesImages(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,b2xk"}}]},
			{"role":"assistant","content":"done","tool_calls":[{"id":"tool-image","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"tool-image","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,bmV3"}}]},
			{"role":"user","content":"summarize"}
		]
	}`)

	parsed := parseOpenAIRequest(payload)
	flattenConversationIntoUserText(parsed)
	if len(parsed.Images) != 2 {
		t.Fatalf("flattened images = %d, want 2", len(parsed.Images))
	}
	if parsed.Images[0].MimeType != "image/png" || parsed.Images[1].MimeType != "image/jpeg" {
		t.Fatalf("flattened image order = %#v", parsed.Images)
	}
}

func TestClaudeToolResultImageReachesCursorReadBinaryOutput(t *testing.T) {
	t.Parallel()

	claudePayload := []byte(`{
		"model":"composer-2.5-fast",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tool-image","name":"read","input":{"path":"shot.png"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-image","content":[
				{"type":"text","text":"Read image file [image/png]"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQIDBA=="}}
			]}]}
		]
	}`)

	translated := claudetoopenai.ConvertClaudeRequestToOpenAI("composer-2.5-fast", claudePayload, true)
	parsed := parseOpenAIRequest(translated)
	if len(parsed.ToolResults) != 1 || len(parsed.ToolResults[0].Images) != 1 {
		t.Fatalf("translated tool result = %#v", parsed.ToolResults)
	}

	messages := encodeCursorExecCompletion(pendingMcpExec{
		ExecMsgId:  9,
		ExecId:     "exec-read",
		ToolCallId: "tool-image",
		Path:       "shot.png",
		Kind:       cursorExecRead,
	}, parsed.ToolResults[0])
	if len(messages) != 2 {
		t.Fatalf("encoded messages = %d, want result and stream close", len(messages))
	}
	exec := mustFindCursorBytesField(t, messages[0], cursorproto.ACM_ExecClientMessage)
	readResult := mustFindCursorBytesField(t, exec, cursorproto.ECM_ReadResult)
	readSuccess := mustFindCursorBytesField(t, readResult, 1)
	if got := mustFindCursorBytesField(t, readSuccess, 5); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("read binary output = %v", got)
	}
}

func TestCursorReadRejectsMultipleBinaryImages(t *testing.T) {
	t.Parallel()

	messages := encodeCursorExecCompletion(pendingMcpExec{
		ExecMsgId: 1,
		ExecId:    "exec-read",
		Path:      "shot.png",
		Kind:      cursorExecRead,
	}, toolResultInfo{
		Content: "two images",
		Images: []cursorproto.ImageData{
			{MimeType: "image/png", Data: []byte{1}},
			{MimeType: "image/png", Data: []byte{2}},
		},
	})
	exec := mustFindCursorBytesField(t, messages[0], cursorproto.ACM_ExecClientMessage)
	readResult := mustFindCursorBytesField(t, exec, cursorproto.ECM_ReadResult)
	readError := mustFindCursorBytesField(t, readResult, 2)
	if got := string(mustFindCursorBytesField(t, readError, 2)); !strings.Contains(got, "multiple images") {
		t.Fatalf("read error = %q", got)
	}
}

func mustFindCursorBytesField(t *testing.T, raw []byte, want protowire.Number) []byte {
	t.Helper()
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatalf("consume tag: %v", protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		if wireType != protowire.BytesType {
			valueLen := protowire.ConsumeFieldValue(number, wireType, raw)
			if valueLen < 0 {
				t.Fatalf("consume field %d: %v", number, protowire.ParseError(valueLen))
			}
			raw = raw[valueLen:]
			continue
		}
		value, valueLen := protowire.ConsumeBytes(raw)
		if valueLen < 0 {
			t.Fatalf("consume bytes field %d: %v", number, protowire.ParseError(valueLen))
		}
		if number == want {
			return value
		}
		raw = raw[valueLen:]
	}
	t.Fatalf("bytes field %d not found", want)
	return nil
}
