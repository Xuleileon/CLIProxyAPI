package executor

import "testing"

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
