package cliproxy

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type prefixIsolationExecutor struct {
	serviceTestPluginExecutor
	provider string
	calls    int
	fail     bool
}

func (e *prefixIsolationExecutor) Identifier() string { return e.provider }
func (e *prefixIsolationExecutor) Execute(context.Context, *coreauth.Auth, ex.Request, ex.Options) (ex.Response, error) {
	e.calls++
	if e.fail {
		return ex.Response{}, fmt.Errorf("upstream connection failed")
	}
	return ex.Response{Payload: []byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)}, nil
}

func TestCommandCodeForcedPrefixPreventsCrossProviderFallback(t *testing.T) {
	const model = "claude-fable-5-1"
	ctx := context.Background()
	cfg := &config.Config{CommandCodeKey: []config.CommandCodeKey{{APIKey: "test-key", Prefix: "command-code", Models: []config.CommandCodeModel{{Name: model}}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	commandCode := &prefixIsolationExecutor{provider: "command-code"}
	claude := &prefixIsolationExecutor{provider: "claude", fail: true}
	manager.RegisterExecutor(commandCode)
	manager.RegisterExecutor(claude)
	service := &Service{cfg: cfg, coreManager: manager}
	commandAuth := &coreauth.Auth{ID: t.Name() + "-cc", Provider: "command-code", Prefix: "command-code", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "auth_kind": "apikey"}}
	claudeAuth := &coreauth.Auth{ID: t.Name() + "-claude", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	reg := registry.GetGlobalRegistry()
	for _, a := range []*coreauth.Auth{commandAuth, claudeAuth} {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	}
	service.registerModelsForAuth(ctx, commandAuth)
	if len(reg.GetModelsForClient(commandAuth.ID)) != 2 {
		t.Fatal("expected legacy prefixed and unprefixed registration")
	}
	cfg.ForceModelPrefix = true
	manager.SetConfig(cfg)
	service.registerModelsForAuth(ctx, commandAuth)
	models := reg.GetModelsForClient(commandAuth.ID)
	if len(models) != 1 || models[0].ID != "command-code/"+model {
		t.Fatalf("unprefixed registration survived reload: %+v", models)
	}
	reg.RegisterClient(claudeAuth.ID, "claude", []*ModelInfo{{ID: model, UserDefined: true}})
	if _, err := manager.Execute(ctx, []string{"claude", "command-code"}, ex.Request{Model: model, Payload: []byte(`{}`)}, ex.Options{}); err == nil {
		t.Fatal("expected original provider failure")
	}
	if claude.calls == 0 || commandCode.calls != 0 {
		t.Fatalf("cross-provider fallback: claude=%d command-code=%d", claude.calls, commandCode.calls)
	}
	if _, err := manager.Execute(ctx, []string{"claude", "command-code"}, ex.Request{Model: "command-code/" + model, Payload: []byte(`{}`)}, ex.Options{}); err != nil {
		t.Fatalf("explicit prefix failed: %v", err)
	}
	if commandCode.calls != 1 {
		t.Fatalf("explicit prefix called Command Code %d times", commandCode.calls)
	}
}
