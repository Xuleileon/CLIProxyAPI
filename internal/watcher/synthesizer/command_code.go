package synthesizer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (s *ConfigSynthesizer) synthesizeCommandCodeKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	out := make([]*coreauth.Auth, 0, len(cfg.CommandCodeKey))
	for i := range cfg.CommandCodeKey {
		entry := cfg.CommandCodeKey[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		baseURL := strings.TrimSpace(entry.BaseURL)
		if baseURL == "" {
			baseURL = config.DefaultCommandCodeBaseURL
		}
		id, token := ctx.IDGenerator.Next("command-code:apikey", key, baseURL)
		attrs := map[string]string{
			"source":   fmt.Sprintf("config:command-code[%s]", token),
			"api_key":  key,
			"base_url": baseURL,
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		if hash := diff.ComputeCommandCodeModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		metadata := map[string]any{}
		if entry.DisableCooling {
			metadata["disable_cooling"] = true
		}
		label := strings.TrimSpace(entry.Name)
		if label == "" {
			label = "command-code-apikey"
		}
		auth := &coreauth.Auth{
			ID:         id,
			Provider:   "command-code",
			Label:      label,
			Prefix:     strings.TrimSpace(entry.Prefix),
			Status:     coreauth.StatusActive,
			ProxyURL:   strings.TrimSpace(entry.ProxyURL),
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  ctx.Now,
			UpdatedAt:  ctx.Now,
		}
		ApplyAuthExcludedModelsMeta(auth, cfg, entry.ExcludedModels, "apikey")
		if len(auth.Metadata) == 0 {
			auth.Metadata = nil
		}
		out = append(out, auth)
	}
	return out
}
