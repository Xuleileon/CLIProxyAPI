package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenCodeGoAPIKeyAliasAndForceMapping(t *testing.T) {
	cfg := &internalconfig.Config{OpenCodeGoKey: []internalconfig.OpenCodeGoKey{{
		APIKey:  "go-key",
		BaseURL: internalconfig.DefaultOpenCodeGoBaseURL,
		Models: []internalconfig.OpenCodeGoModel{{
			Name:         "minimax-m3",
			Alias:        "mini-go",
			Protocol:     "anthropic",
			ForceMapping: true,
		}},
	}}}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	auth := &Auth{ID: "opencode-go:test", Provider: "opencode-go", Attributes: map[string]string{"api_key": "go-key", "base_url": internalconfig.DefaultOpenCodeGoBaseURL}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if got := manager.applyAPIKeyModelAlias(auth, "mini-go"); got != "minimax-m3" {
		t.Fatalf("alias = %q, want minimax-m3", got)
	}
	result := manager.resolveAPIKeyModelAliasWithResult(auth, "mini-go")
	if result.UpstreamModel != "minimax-m3" || !result.ForceMapping || result.OriginalAlias != "mini-go" {
		t.Fatalf("result = %+v", result)
	}
}
