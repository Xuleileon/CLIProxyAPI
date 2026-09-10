package config

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const (
	// DefaultCommandCodeBaseURL is the official Command Code API endpoint.
	DefaultCommandCodeBaseURL = "https://api.commandcode.ai"
)

// CommandCodeKey represents a Command Code subscription credential.
type CommandCodeKey struct {
	Name           string             `yaml:"name,omitempty" json:"name,omitempty"`
	APIKey         string             `yaml:"api-key" json:"api-key"`
	Priority       int                `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prefix         string             `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	BaseURL        string             `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	ProxyURL       string             `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	Models         []CommandCodeModel `yaml:"models,omitempty" json:"models,omitempty"`
	Headers        map[string]string  `yaml:"headers,omitempty" json:"headers,omitempty"`
	ExcludedModels []string           `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	DisableCooling bool               `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`
}

func (k CommandCodeKey) GetAPIKey() string   { return k.APIKey }
func (k CommandCodeKey) GetBaseURL() string  { return k.BaseURL }
func (k CommandCodeKey) GetPrefix() string   { return k.Prefix }
func (k CommandCodeKey) GetProxyURL() string { return k.ProxyURL }

// CommandCodeModel maps a client-facing alias to an upstream model .
type CommandCodeModel struct {
	Name         string `yaml:"name" json:"name"`
	Alias        string `yaml:"alias" json:"alias"`
	DisplayName  string `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	ForceMapping bool   `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
}

func (m CommandCodeModel) GetName() string        { return m.Name }
func (m CommandCodeModel) GetAlias() string       { return m.Alias }
func (m CommandCodeModel) GetDisplayName() string { return m.DisplayName }
func (m CommandCodeModel) GetThinking() *registry.ThinkingSupport {
	return nil
}
func (m CommandCodeModel) GetForceMapping() bool { return m.ForceMapping }

// SanitizeCommandCodeKeys normalizes credentials and drops entries without an API key.
func (cfg *Config) SanitizeCommandCodeKeys() {
	if cfg == nil || len(cfg.CommandCodeKey) == 0 {
		return
	}
	out := make([]CommandCodeKey, 0, len(cfg.CommandCodeKey))
	for i := range cfg.CommandCodeKey {
		entry := cfg.CommandCodeKey[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		if entry.APIKey == "" {
			continue
		}
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BaseURL = strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")
		if entry.BaseURL == "" {
			entry.BaseURL = DefaultCommandCodeBaseURL
		}
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		models := make([]CommandCodeModel, 0, len(entry.Models))
		for j := range entry.Models {
			model := entry.Models[j]
			model.Name = strings.TrimSpace(model.Name)
			model.Alias = strings.TrimSpace(model.Alias)
			model.DisplayName = strings.TrimSpace(model.DisplayName)
			if model.Name == "" && model.Alias == "" {
				continue
			}
			models = append(models, model)
		}
		entry.Models = models
		out = append(out, entry)
	}
	cfg.CommandCodeKey = out
}
