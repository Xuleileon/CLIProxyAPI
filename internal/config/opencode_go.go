package config

import "strings"

const (
	// DefaultOpenCodeGoBaseURL is the official OpenCode Go API endpoint.
	DefaultOpenCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

	OpenCodeGoProtocolOpenAI    = "openai"
	OpenCodeGoProtocolClaude    = "anthropic"
	OpenCodeGoProtocolResponses = "responses"
)

// OpenCodeGoKey represents an OpenCode Go subscription credential.
type OpenCodeGoKey struct {
	Name           string            `yaml:"name,omitempty" json:"name,omitempty"`
	APIKey         string            `yaml:"api-key" json:"api-key"`
	Priority       int               `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prefix         string            `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	BaseURL        string            `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	ProxyURL       string            `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	Models         []OpenCodeGoModel `yaml:"models,omitempty" json:"models,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ExcludedModels []string          `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	DisableCooling bool              `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`
}

func (k OpenCodeGoKey) GetAPIKey() string  { return k.APIKey }
func (k OpenCodeGoKey) GetBaseURL() string { return k.BaseURL }

// OpenCodeGoModel maps a client-facing alias to an upstream model and wire protocol.
type OpenCodeGoModel struct {
	Name         string `yaml:"name" json:"name"`
	Alias        string `yaml:"alias" json:"alias"`
	DisplayName  string `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	Protocol     string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	ForceMapping bool   `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
}

func (m OpenCodeGoModel) GetName() string        { return m.Name }
func (m OpenCodeGoModel) GetAlias() string       { return m.Alias }
func (m OpenCodeGoModel) GetDisplayName() string { return m.DisplayName }
func (m OpenCodeGoModel) GetForceMapping() bool  { return m.ForceMapping }

// NormalizeOpenCodeGoProtocol converts accepted protocol aliases to canonical values.
func NormalizeOpenCodeGoProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "":
		return ""
	case "openai", "chat", "chat-completions", "chat_completions":
		return OpenCodeGoProtocolOpenAI
	case "anthropic", "claude", "messages":
		return OpenCodeGoProtocolClaude
	case "responses", "openai-responses", "openai_response", "openai-response":
		return OpenCodeGoProtocolResponses
	default:
		return ""
	}
}

// SanitizeOpenCodeGoKeys normalizes credentials and drops entries without an API key.
func (cfg *Config) SanitizeOpenCodeGoKeys() {
	if cfg == nil || len(cfg.OpenCodeGoKey) == 0 {
		return
	}
	out := make([]OpenCodeGoKey, 0, len(cfg.OpenCodeGoKey))
	for i := range cfg.OpenCodeGoKey {
		entry := cfg.OpenCodeGoKey[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		if entry.APIKey == "" {
			continue
		}
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BaseURL = strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")
		if entry.BaseURL == "" {
			entry.BaseURL = DefaultOpenCodeGoBaseURL
		}
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		models := make([]OpenCodeGoModel, 0, len(entry.Models))
		for j := range entry.Models {
			model := entry.Models[j]
			model.Name = strings.TrimSpace(model.Name)
			model.Alias = strings.TrimSpace(model.Alias)
			model.DisplayName = strings.TrimSpace(model.DisplayName)
			if model.Name == "" && model.Alias == "" {
				continue
			}
			rawProtocol := strings.TrimSpace(model.Protocol)
			model.Protocol = NormalizeOpenCodeGoProtocol(rawProtocol)
			if rawProtocol != "" && model.Protocol == "" {
				continue
			}
			models = append(models, model)
		}
		entry.Models = models
		out = append(out, entry)
	}
	cfg.OpenCodeGoKey = out
}
