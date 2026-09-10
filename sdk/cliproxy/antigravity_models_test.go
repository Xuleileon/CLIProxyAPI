package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const antigravityTieredCatalogFixture = `{
	"models": {
		"gemini-3.6-flash-high": {},
		"gemini-3.8-flash-tiered": {"maxTokens":1048576,"maxOutputTokens":65536,"minThinkingBudget":32,"thinkingBudget":-1,"supportsThinking":true,"supportsImages":true,"supportsVideo":true,"supportedMimeTypes":{"audio/wav":true,"audio/mp3":true}},
		"tab_internal": {}
	},
	"tieredModelIds":{"flash":["gemini-3.8-flash-tiered","missing-tiered","gemini-3.8-flash-tiered"]},
	"webSearchModelIds":["gemini-3.8-flash-tiered"]
}`

func TestAntigravityCatalogReconcilesLiveTieredModels(t *testing.T) {
	stale := []*ModelInfo{{ID: "gemini-3.8-flash-high"}, {ID: "gemini-3.6-flash-high"}}
	hints := parseAntigravityModelCapabilityHints([]byte(antigravityTieredCatalogFixture))
	models := applyAntigravityFetchedModelCapabilities(stale, hints)
	if len(models) != 2 || models[0].ID != "gemini-3.6-flash-high" || models[1].ID != "gemini-3.8-flash-tiered" {
		t.Fatalf("unexpected live catalog: %+v", models)
	}
	model := models[1]
	if model.ContextLength != 1048576 || model.MaxCompletionTokens != 65536 || !model.SupportsWebSearch {
		t.Fatalf("lost upstream capabilities: %+v", model)
	}
	if model.Thinking == nil || model.Thinking.Min != 32 || !model.Thinking.DynamicAllowed || model.Thinking.ZeroAllowed {
		t.Fatalf("incorrect thinking capabilities: %+v", model.Thinking)
	}
	if !reflect.DeepEqual(model.SupportedInputModalities, []string{"text", "image", "audio", "video"}) {
		t.Fatalf("lost input modalities: %v", model.SupportedInputModalities)
	}
	if stale[0].ID != "gemini-3.8-flash-high" {
		t.Fatal("mutated the shared fallback catalog")
	}
	if fallback := applyAntigravityFetchedModelCapabilities(stale, antigravityModelCapabilityHints{}); len(fallback) != 2 {
		t.Fatal("a failed first discovery must retain the fallback catalog")
	}
}

func TestAntigravityCatalogRetainsLastGoodAccountSnapshot(t *testing.T) {
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path != antigravityModelsPath || r.Header.Get("Authorization") != "Bearer test-access" {
			t.Errorf("incorrect discovery request: %s", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["project"] == "" {
			t.Errorf("missing project-scoped discovery payload: %v", err)
		}
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_, _ = w.Write([]byte(`{"models":{}}`))
		case 3:
			_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.8-flash-tiered"]}`))
		default:
			_, _ = w.Write([]byte(antigravityTieredCatalogFixture))
		}
	}))
	defer server.Close()
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{"access_token": "test-access", "project_id": "project-a", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	first := service.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if len(first.Models) != 3 {
		t.Fatalf("live catalog not captured: %+v", first)
	}
	for _, failureMode := range []int32{1, 2, 3} {
		mode.Store(failureMode)
		next := service.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
		if !reflect.DeepEqual(first, next) {
			t.Fatalf("failure mode %d replaced last good catalog", failureMode)
		}
	}
	other := auth.Clone()
	other.ID += "-other"
	if hints := service.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), other); len(hints.Models) != 0 {
		t.Fatal("catalog leaked between accounts")
	}
	other = auth.Clone()
	other.Metadata["project_id"] = "project-b"
	if hints := service.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), other); len(hints.Models) != 0 {
		t.Fatal("catalog leaked between projects")
	}
}
