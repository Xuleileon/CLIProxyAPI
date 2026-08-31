package management

import (
	"context"
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAntigravityOAuthClientValueFallsBackToDefaults(t *testing.T) {
	t.Setenv(antigravityOAuthClientIDEnv, "")
	t.Setenv(antigravityOAuthClientSecretEnv, "")

	if got := antigravityOAuthClientValue(nil, "client_id", antigravityOAuthClientIDEnv); got != antigravityauth.DefaultClientID {
		t.Fatalf("client_id = %q, want default", got)
	}
	if got := antigravityOAuthClientValue(nil, "client_secret", antigravityOAuthClientSecretEnv); got != antigravityauth.DefaultClientSecret {
		t.Fatalf("client_secret = %q, want default", got)
	}
}

func TestReusableAntigravityProjectIDRequiresSameAccount(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "antigravity-user@example.com.json",
		FileName: "antigravity-user@example.com.json",
		Provider: "antigravity",
		Metadata: map[string]any{
			"email":      "user@example.com",
			"project_id": "existing-project",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	if got := h.reusableAntigravityProjectID("user@example.com", record.FileName); got != "existing-project" {
		t.Fatalf("same account project = %q", got)
	}
	if got := h.reusableAntigravityProjectID("other@example.com", "antigravity-other@example.com.json"); got != "" {
		t.Fatalf("different account project = %q, want empty", got)
	}
}
