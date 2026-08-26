package executor

import (
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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

func TestBuildRunRequestParams_PrefixesClaudeCodeToolNames(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"claude-sonnet-5-thinking-high","messages":[],"tools":[` +
		`{"type":"function","function":{"name":"Agent","description":"spawn","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"Bash","description":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}},` +
		`{"type":"function","function":{"name":"Read","description":"read","parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}},` +
		`{"type":"function","function":{"name":"Write","description":"write","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"WebSearch","description":"search","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"Workflow","description":"workflow","parameters":{"type":"object"}}}` +
		`]}`)
	params, err := buildRunRequestParams(parseOpenAIRequest(payload), "conv-claude-code", "claude-sonnet-5-thinking-high")
	if err != nil {
		t.Fatalf("build params: %v", err)
	}
	if params.AgentMode != cursorproto.AgentModeAgent {
		t.Fatalf("mode = %d, want agent", params.AgentMode)
	}
	if len(params.McpTools) != 6 {
		t.Fatalf("tools = %d, want 6", len(params.McpTools))
	}
	want := map[string]string{
		"acp_Agent":     "Agent",
		"acp_Bash":      "Bash",
		"acp_Read":      "Read",
		"acp_Write":     "Write",
		"acp_WebSearch": "WebSearch",
		"acp_Workflow":  "Workflow",
	}
	for _, tool := range params.McpTools {
		if client, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected upstream MCP name %q", tool.Name)
		} else if got := helps.UnprefixCursorACPName(tool.Name); got != client {
			t.Fatalf("client name for %q = %q, want %q", tool.Name, got, client)
		}
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
