package config

import "strings"

// AmpModelMapping maps an incoming Amp model to an upstream model.
type AmpModelMapping struct {
	From  string `yaml:"from" json:"from"`
	To    string `yaml:"to" json:"to"`
	Regex bool   `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// AmpCode groups the fork-maintained Amp integration settings.
type AmpCode struct {
	UpstreamURL                   string                   `yaml:"upstream-url" json:"upstream-url"`
	UpstreamAPIKey                string                   `yaml:"upstream-api-key" json:"upstream-api-key"`
	UpstreamAPIKeys               []AmpUpstreamAPIKeyEntry `yaml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`
	RestrictManagementToLocalhost bool                     `yaml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`
	ModelMappings                 []AmpModelMapping        `yaml:"model-mappings" json:"model-mappings"`
	ForceModelMappings            bool                     `yaml:"force-model-mappings" json:"force-model-mappings"`
}

// AmpUpstreamAPIKeyEntry maps downstream API keys to one Amp upstream key.
type AmpUpstreamAPIKeyEntry struct {
	UpstreamAPIKey string   `yaml:"upstream-api-key" json:"upstream-api-key"`
	APIKeys        []string `yaml:"api-keys" json:"api-keys"`
}

// KiroKey represents a Kiro credential configuration.
type KiroKey struct {
	TokenFile         string `yaml:"token-file,omitempty" json:"token-file,omitempty"`
	AccessToken       string `yaml:"access-token,omitempty" json:"access-token,omitempty"`
	RefreshToken      string `yaml:"refresh-token,omitempty" json:"refresh-token,omitempty"`
	ProfileArn        string `yaml:"profile-arn,omitempty" json:"profile-arn,omitempty"`
	Region            string `yaml:"region,omitempty" json:"region,omitempty"`
	StartURL          string `yaml:"start-url,omitempty" json:"start-url,omitempty"`
	ProxyURL          string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	AgentTaskType     string `yaml:"agent-task-type,omitempty" json:"agent-task-type,omitempty"`
	PreferredEndpoint string `yaml:"preferred-endpoint,omitempty" json:"preferred-endpoint,omitempty"`
}

// KiroFingerprintConfig pins the Kiro client fingerprint when configured.
type KiroFingerprintConfig struct {
	OIDCSDKVersion      string `yaml:"oidc-sdk-version,omitempty" json:"oidc-sdk-version,omitempty"`
	RuntimeSDKVersion   string `yaml:"runtime-sdk-version,omitempty" json:"runtime-sdk-version,omitempty"`
	StreamingSDKVersion string `yaml:"streaming-sdk-version,omitempty" json:"streaming-sdk-version,omitempty"`
	OSType              string `yaml:"os-type,omitempty" json:"os-type,omitempty"`
	OSVersion           string `yaml:"os-version,omitempty" json:"os-version,omitempty"`
	NodeVersion         string `yaml:"node-version,omitempty" json:"node-version,omitempty"`
	KiroVersion         string `yaml:"kiro-version,omitempty" json:"kiro-version,omitempty"`
	KiroHash            string `yaml:"kiro-hash,omitempty" json:"kiro-hash,omitempty"`
}

// KiroRateLimitConfig configures Kiro request pacing and backoff.
type KiroRateLimitConfig struct {
	Enabled           *bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	MinTokenInterval  string  `yaml:"min-token-interval,omitempty" json:"min-token-interval,omitempty"`
	MaxTokenInterval  string  `yaml:"max-token-interval,omitempty" json:"max-token-interval,omitempty"`
	DailyMaxRequests  int     `yaml:"daily-max-requests,omitempty" json:"daily-max-requests,omitempty"`
	JitterPercent     float64 `yaml:"jitter-percent,omitempty" json:"jitter-percent,omitempty"`
	BackoffBase       string  `yaml:"backoff-base,omitempty" json:"backoff-base,omitempty"`
	BackoffMax        string  `yaml:"backoff-max,omitempty" json:"backoff-max,omitempty"`
	BackoffMultiplier float64 `yaml:"backoff-multiplier,omitempty" json:"backoff-multiplier,omitempty"`
	SuspendCooldown   string  `yaml:"suspend-cooldown,omitempty" json:"suspend-cooldown,omitempty"`
}

// SanitizeKiroKeys trims Kiro credential fields loaded from configuration.
func (cfg *Config) SanitizeKiroKeys() {
	if cfg == nil {
		return
	}
	for i := range cfg.KiroKey {
		entry := &cfg.KiroKey[i]
		entry.TokenFile = strings.TrimSpace(entry.TokenFile)
		entry.AccessToken = strings.TrimSpace(entry.AccessToken)
		entry.RefreshToken = strings.TrimSpace(entry.RefreshToken)
		entry.ProfileArn = strings.TrimSpace(entry.ProfileArn)
		entry.Region = strings.TrimSpace(entry.Region)
		entry.StartURL = strings.TrimSpace(entry.StartURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.AgentTaskType = strings.TrimSpace(entry.AgentTaskType)
		entry.PreferredEndpoint = strings.TrimSpace(entry.PreferredEndpoint)
	}
}
