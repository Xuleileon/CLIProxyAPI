package cliproxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

func applyKiroRuntimeConfig(cfg *config.Config) {
	kiroauth.InitRateLimiterConfig(cfg)
	kiroauth.InitSystemPromptInjectConfig(cfg)
	kiroauth.InitTruncationDetectorConfig(cfg)
	kiroauth.InitExtractThinkingTagConfig(cfg)
}

// GetWatcher returns the service watcher for embedded integrations.
func (s *Service) GetWatcher() *WatcherWrapper {
	if s == nil {
		return nil
	}
	return s.watcher
}

func (s *Service) effectiveAuthExcludedModels(auth *coreauth.Auth, provider, authKind string, fallback []string) []string {
	return mergeExcludedModels(
		fallback,
		s.oauthExcludedModels(provider, authKind),
		excludedModelsFromAuthAttributes(auth),
	)
}

func excludedModelsFromAuthAttributes(auth *coreauth.Auth) []string {
	if auth == nil || auth.Attributes == nil {
		return nil
	}
	value := strings.TrimSpace(auth.Attributes["excluded_models"])
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func mergeExcludedModels(lists ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, list := range lists {
		for _, item := range list {
			trimmed := strings.ToLower(strings.TrimSpace(item))
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) resolveConfigOpenCodeGoKey(auth *coreauth.Auth) *config.OpenCodeGoKey {
	if auth == nil || s == nil || s.cfg == nil {
		return nil
	}
	apiKey, baseURL := "", ""
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/")
	}
	for i := range s.cfg.OpenCodeGoKey {
		entry := &s.cfg.OpenCodeGoKey[i]
		if apiKey != "" && !strings.EqualFold(apiKey, strings.TrimSpace(entry.APIKey)) {
			continue
		}
		if baseURL != "" && !strings.EqualFold(baseURL, strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")) {
			continue
		}
		return entry
	}
	return nil
}

func buildOpenCodeGoConfigModels(entry *config.OpenCodeGoKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	models := buildConfigModels(entry.Models, "opencode-go", "opencode-go")
	for i := range models {
		if models[i] == nil {
			continue
		}
		for j := range entry.Models {
			configured := entry.Models[j]
			alias := strings.TrimSpace(configured.Alias)
			if alias == "" {
				alias = strings.TrimSpace(configured.Name)
			}
			if !strings.EqualFold(alias, models[i].ID) {
				continue
			}
			protocol := config.NormalizeOpenCodeGoProtocol(configured.Protocol)
			if protocol == "" {
				protocol = registry.OpenCodeGoProtocolForModel(configured.Name)
			}
			switch protocol {
			case config.OpenCodeGoProtocolClaude:
				models[i].Type = "claude"
				models[i].SupportedEndpoints = []string{"/messages"}
			case config.OpenCodeGoProtocolResponses:
				models[i].Type = "openai-response"
				models[i].SupportedEndpoints = []string{"/responses"}
			default:
				models[i].Type = "openai"
				models[i].SupportedEndpoints = []string{"/chat/completions"}
			}
			break
		}
	}
	return models
}

func (s *Service) fetchKiroModels(a *coreauth.Auth) []*ModelInfo {
	if a == nil {
		return registry.GetKiroModels()
	}
	tokenData := s.extractKiroTokenData(a)
	if tokenData == nil || tokenData.AccessToken == "" {
		return registry.GetKiroModels()
	}
	kAuth := kiroauth.NewKiroAuth(s.cfg)
	if kAuth == nil {
		return registry.GetKiroModels()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	apiModels, err := kAuth.ListAvailableModels(ctx, tokenData)
	if err != nil {
		log.Warnf("kiro: failed to fetch dynamic models: %v, using static models", err)
		return registry.GetKiroModels()
	}
	if len(apiModels) == 0 {
		return registry.GetKiroModels()
	}
	models := convertKiroAPIModels(apiModels)
	return generateKiroAgenticVariants(models)
}

func (s *Service) extractKiroTokenData(a *coreauth.Auth) *kiroauth.KiroTokenData {
	if a == nil {
		return nil
	}
	var accessToken, profileArn, refreshToken string
	if a.Attributes != nil {
		accessToken = strings.TrimSpace(a.Attributes["access_token"])
		profileArn = strings.TrimSpace(a.Attributes["profile_arn"])
		refreshToken = strings.TrimSpace(a.Attributes["refresh_token"])
	}
	if accessToken == "" && a.Metadata != nil {
		accessToken, _ = a.Metadata["access_token"].(string)
		profileArn, _ = a.Metadata["profile_arn"].(string)
		refreshToken, _ = a.Metadata["refresh_token"].(string)
		accessToken = strings.TrimSpace(accessToken)
		profileArn = strings.TrimSpace(profileArn)
		refreshToken = strings.TrimSpace(refreshToken)
	}
	if accessToken == "" {
		return nil
	}
	return &kiroauth.KiroTokenData{AccessToken: accessToken, ProfileArn: profileArn, RefreshToken: refreshToken}
}

func convertKiroAPIModels(apiModels []*kiroauth.KiroModel) []*ModelInfo {
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(apiModels))
	for _, model := range apiModels {
		if model == nil || model.ModelID == "" {
			continue
		}
		contextLength := 200000
		if model.MaxInputTokens > 0 {
			contextLength = model.MaxInputTokens
		}
		models = append(models, &ModelInfo{
			ID:                  "kiro-" + normalizeKiroModelID(model.ModelID),
			Object:              "model",
			Created:             now,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         formatKiroDisplayName(model.ModelName, model.RateMultiplier),
			Description:         model.Description,
			ContextLength:       contextLength,
			MaxCompletionTokens: 64000,
			Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		})
	}
	return models
}

func normalizeKiroModelID(modelID string) string {
	modelID = strings.TrimPrefix(modelID, "anthropic.")
	modelID = strings.TrimPrefix(modelID, "amazon.")
	modelID = strings.ReplaceAll(modelID, ".", "-")
	modelID = strings.ReplaceAll(modelID, "_", "-")
	return strings.ToLower(modelID)
}

func formatKiroDisplayName(modelName string, rateMultiplier float64) string {
	if modelName == "" {
		return ""
	}
	displayName := "Kiro " + modelName
	if rateMultiplier > 0 && rateMultiplier != 1 {
		displayName += fmt.Sprintf(" (%.1fx credit)", rateMultiplier)
	}
	return displayName
}

func filterAgenticVariants(models []*ModelInfo) []*ModelInfo {
	result := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil && strings.HasSuffix(model.ID, "-agentic") {
			continue
		}
		result = append(result, model)
	}
	return result
}

func generateKiroAgenticVariants(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 || !kirocommon.IsSystemPromptInjectEnabled() {
		return models
	}
	result := make([]*ModelInfo, 0, len(models)*2)
	result = append(result, models...)
	for _, model := range models {
		if model == nil || strings.HasSuffix(model.ID, "-agentic") || strings.Contains(model.ID, "-auto") {
			continue
		}
		agentic := *model
		agentic.ID += "-agentic"
		agentic.DisplayName += " (Agentic)"
		agentic.Description += " - Optimized for coding agents (chunked writes)"
		if model.Thinking != nil {
			thinking := *model.Thinking
			agentic.Thinking = &thinking
		}
		result = append(result, &agentic)
	}
	return result
}
