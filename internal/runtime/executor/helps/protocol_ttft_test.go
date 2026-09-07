package helps

import "testing"

func TestProtocolTokenEventsIgnoreScaffolding(t *testing.T) {
	tests := []struct {
		name             string
		classify         func([]byte) bool
		metadata, output string
	}{
		{"chat", IsChatTokenEvent, `data: {"choices":[{"delta":{"role":"assistant"}}]}`, `data: {"choices":[{"delta":{"content":"hello"}}]}`},
		{"claude", IsClaudeTokenEvent, `data: {"type":"message_start","message":{"content":[]}}`, `data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"consider"}}`},
		{"gemini", IsGeminiTokenEvent, `data: {"usageMetadata":{"promptTokenCount":20}}`, `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`},
		{"antigravity", IsGeminiTokenEvent, `data: {"response":{"usageMetadata":{"promptTokenCount":20}}}`, `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.classify([]byte(tt.metadata)) {
				t.Fatal("metadata counted as output")
			}
			if !tt.classify([]byte(tt.output)) {
				t.Fatal("output was not counted")
			}
		})
	}
}
