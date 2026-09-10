package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityModelBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath        = "/v1internal:fetchAvailableModels"
)

type antigravityFetchAvailableModelsResponse struct {
	WebSearchModelIDs []string                           `json:"webSearchModelIds"`
	Models            map[string]antigravityFetchedModel `json:"models"`
	TieredModelIDs    map[string][]string                `json:"tieredModelIds"`
}

type antigravityFetchedModel struct {
	MaxTokens          int             `json:"maxTokens"`
	MaxOutputTokens    int             `json:"maxOutputTokens"`
	MinThinkingBudget  int             `json:"minThinkingBudget"`
	ThinkingBudget     int             `json:"thinkingBudget"`
	SupportsThinking   bool            `json:"supportsThinking"`
	SupportsImages     bool            `json:"supportsImages"`
	SupportsVideo      bool            `json:"supportsVideo"`
	SupportedMimeTypes map[string]bool `json:"supportedMimeTypes"`
}

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
	Models            map[string]antigravityFetchedModel
	TieredModelIDs    []string
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || auth.Metadata == nil {
		return antigravityModelCapabilityHints{}
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return antigravityModelCapabilityHints{}
	}

	projectID, _ := auth.Metadata["project_id"].(string)
	cacheKey := auth.ID + "\x00" + projectID + "\x00" + resolveAntigravityModelBaseURL(auth)
	body, _ := json.Marshal(map[string]string{"project": projectID})
	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(s.antigravityModelFetchProxyURL(auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}
	var capabilityOnlyHints antigravityModelCapabilityHints

	for _, baseURL := range antigravityModelBaseURLs(auth) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(string(body)))
		if errReq != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Close = true
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", misc.AntigravityUserAgent())

		resp, errDo := client.Do(req)
		if errDo != nil {
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
		if errRead != nil {
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			continue
		}
		hints := parseAntigravityModelCapabilityHints(body)
		if len(hints.Models) > 0 {
			if auth.ID != "" {
				s.antigravityModelHints.Store(cacheKey, hints)
			}
			return hints
		}
		if len(hints.WebSearchModelIDs) > 0 {
			capabilityOnlyHints = hints
		}
	}
	if auth.ID != "" {
		if cached, ok := s.antigravityModelHints.Load(cacheKey); ok {
			return cached.(antigravityModelCapabilityHints)
		}
	}
	return capabilityOnlyHints
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily, antigravityModelBaseURLProd}
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func parseAntigravityModelCapabilityHints(body []byte) antigravityModelCapabilityHints {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityModelCapabilityHints{}
	}
	webSearchModels := make(map[string]struct{}, len(parsed.WebSearchModelIDs))
	for _, modelID := range parsed.WebSearchModelIDs {
		modelID = normalizeAntigravityFetchedModelID(modelID)
		if modelID != "" {
			webSearchModels[modelID] = struct{}{}
		}
	}
	tiered := make([]string, 0)
	seen := make(map[string]bool)
	for _, ids := range parsed.TieredModelIDs {
		for _, id := range ids {
			if _, exists := parsed.Models[id]; exists && !seen[id] {
				tiered = append(tiered, id)
				seen[id] = true
			}
		}
	}
	sort.Strings(tiered)
	return antigravityModelCapabilityHints{WebSearchModelIDs: webSearchModels, Models: parsed.Models, TieredModelIDs: tiered}
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(hints.Models) > 0 {
		available := make([]*ModelInfo, 0, len(models)+len(hints.TieredModelIDs))
		seen := make(map[string]bool)
		tieredIDs := append([]string(nil), hints.TieredModelIDs...)
		for _, model := range models {
			if model == nil {
				continue
			}
			// Retire an obsolete static high ID only when this account advertises
			// the tiered ID for that exact Gemini version. Partial catalogs must
			// not remove unrelated models or capability-only registrations.
			if _, exists := hints.Models[model.ID]; !exists && strings.HasPrefix(model.ID, "gemini-") && strings.HasSuffix(model.ID, "-high") {
				tieredID := strings.TrimSuffix(model.ID, "-high") + "-tiered"
				if _, available := hints.Models[tieredID]; available {
					tieredIDs = append(tieredIDs, tieredID)
					continue
				}
			}
			available = append(available, model)
			seen[model.ID] = true
		}
		// Only add explicitly advertised tiered generation models, not internal tab/chat models.
		for _, id := range tieredIDs {
			if seen[id] {
				continue
			}
			fetched := hints.Models[id]
			model := &ModelInfo{ID: id, Name: id, Object: "model", OwnedBy: "antigravity", Type: "antigravity",
				DisplayName: id, ContextLength: fetched.MaxTokens, MaxCompletionTokens: fetched.MaxOutputTokens,
				SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}
			if fetched.SupportsImages {
				model.SupportedInputModalities = append(model.SupportedInputModalities, "image")
			}
			for mime, supported := range fetched.SupportedMimeTypes {
				if supported && strings.HasPrefix(mime, "audio/") {
					model.SupportedInputModalities = append(model.SupportedInputModalities, "audio")
					break
				}
			}
			if fetched.SupportsVideo {
				model.SupportedInputModalities = append(model.SupportedInputModalities, "video")
			}
			if fetched.SupportsThinking {
				model.Thinking = &registry.ThinkingSupport{Min: fetched.MinThinkingBudget, Max: max(0, fetched.MaxOutputTokens-1),
					DynamicAllowed: fetched.ThinkingBudget == -1, ZeroAllowed: fetched.MinThinkingBudget == 0}
			}
			available = append(available, model)
			seen[id] = true
		}
		models = available
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}
