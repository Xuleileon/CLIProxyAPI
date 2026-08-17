package registry

import "strings"

const (
	openCodeGoProtocolOpenAI    = "openai"
	openCodeGoProtocolClaude    = "anthropic"
	openCodeGoProtocolResponses = "responses"
)

var openCodeGoAnthropicModels = map[string]struct{}{
	"minimax-m3": {}, "minimax-m2.7": {}, "minimax-m2.5": {},
	"qwen3.8-max": {}, "qwen3.7-max": {}, "qwen3.7-plus": {}, "qwen3.6-plus": {}, "qwen3.5-plus": {},
}

var openCodeGoModelIDs = []string{
	"minimax-m3", "minimax-m2.7", "minimax-m2.5",
	"kimi-k3", "kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5",
	"glm-5.2", "glm-5.1", "glm-5",
	"deepseek-v4-pro", "deepseek-v4-flash",
	"qwen3.7-max", "qwen3.8-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus",
	"mimo-v2-pro", "mimo-v2-omni", "mimo-v2.5-pro", "mimo-v2.5",
	"hy3", "hy3-preview", "gpt-5.6-luna", "grok-4.5",
}

// OpenCodeGoProtocolForModel returns the official wire protocol for a built-in model.
// Unknown models use OpenAI Chat Completions, matching the provider catalog default.
func OpenCodeGoProtocolForModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "gpt-5.6-luna" {
		return openCodeGoProtocolResponses
	}
	if _, ok := openCodeGoAnthropicModels[model]; ok {
		return openCodeGoProtocolClaude
	}
	return openCodeGoProtocolOpenAI
}

// GetOpenCodeGoModels returns the OpenCode Go subscription catalog.
func GetOpenCodeGoModels() []*ModelInfo {
	models := make([]*ModelInfo, 0, len(openCodeGoModelIDs))
	for _, id := range openCodeGoModelIDs {
		protocol := OpenCodeGoProtocolForModel(id)
		endpoint := "/chat/completions"
		modelType := "openai"
		if protocol == openCodeGoProtocolClaude {
			endpoint = "/messages"
			modelType = "claude"
		} else if protocol == openCodeGoProtocolResponses {
			endpoint = "/responses"
			modelType = "openai-response"
		}
		models = append(models, &ModelInfo{
			ID:                        id,
			Object:                    "model",
			OwnedBy:                   "opencode-go",
			Type:                      modelType,
			DisplayName:               id,
			Version:                   id,
			SupportedEndpoints:        []string{endpoint},
			SupportedInputModalities:  []string{"text"},
			SupportedOutputModalities: []string{"text"},
			Thinking:                  &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		})
	}
	return models
}
