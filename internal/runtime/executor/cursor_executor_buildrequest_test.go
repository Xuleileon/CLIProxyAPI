package executor

import (
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func TestBuildRunRequestParams_ModelOverride(t *testing.T) {
	tests := []struct {
		name        string
		parsedModel string
		override    string
		wantModelID string
		wantMaxMode bool
	}{
		{
			name:        "alias override preserves client model",
			parsedModel: "cursor/composer-2.5",
			override:    "composer-2.5",
			wantModelID: "composer-2.5",
		},
		{
			name:        "empty override falls back to parsed model",
			parsedModel: "composer-2.5",
			wantModelID: "composer-2.5",
		},
		{
			name:        "whitespace override falls back to parsed model",
			parsedModel: "composer-2.5",
			override:    " \t ",
			wantModelID: "composer-2.5",
		},
		{
			name:        "override supplies missing parsed model",
			override:    "composer-2.5",
			wantModelID: "composer-2.5",
		},
		{
			name:        "maxmode suffix stripped and enables max mode",
			override:    "composer-2.5-maxmode",
			wantModelID: "composer-2.5",
			wantMaxMode: true,
		},
		{
			name:        "cursor grok requires max mode",
			override:    "cursor-grok-4.5-medium",
			wantModelID: "cursor-grok-4.5-medium",
			wantMaxMode: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := &parsedOpenAIRequest{Model: tc.parsedModel}
			params, err := buildRunRequestParams(parsed, "conv-123", tc.override)
			if err != nil {
				t.Fatalf("build params: %v", err)
			}

			if params.ModelId != tc.wantModelID {
				t.Errorf("ModelId = %q, want %q", params.ModelId, tc.wantModelID)
			}
			if params.MaxMode != tc.wantMaxMode {
				t.Errorf("MaxMode = %v, want %v", params.MaxMode, tc.wantMaxMode)
			}
			if params.ConversationId != "conv-123" {
				t.Errorf("ConversationId = %q, want %q", params.ConversationId, "conv-123")
			}
			if parsed.Model != tc.parsedModel {
				t.Errorf("parsed.Model = %q, want %q", parsed.Model, tc.parsedModel)
			}
		})
	}
}

func TestBuildRunRequestParams_SelectsModeFromToolCapability(t *testing.T) {
	t.Parallel()

	withoutTools, err := buildRunRequestParams(&parsedOpenAIRequest{}, "conv-ask", "composer-2.5")
	if err != nil {
		t.Fatalf("build params without tools: %v", err)
	}
	if withoutTools.AgentMode != cursorproto.AgentModeAsk {
		t.Fatalf("mode without tools = %d, want ask", withoutTools.AgentMode)
	}
	withTools, err := buildRunRequestParams(parseOpenAIRequest([]byte(`{"model":"composer-2.5","messages":[],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`)), "conv-agent", "composer-2.5")
	if err != nil {
		t.Fatalf("build params with tools: %v", err)
	}
	if withTools.AgentMode != cursorproto.AgentModeAgent {
		t.Fatalf("mode with tools = %d, want agent", withTools.AgentMode)
	}
}
