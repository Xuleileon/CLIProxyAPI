package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestOpenCodeGoKeysExposeEffectiveModelCount(t *testing.T) {
	h := &Handler{cfg: &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{{APIKey: "go-key"}}}}
	entries := h.openCodeGoKeysWithAuthIndex()
	if len(entries) != 1 || entries[0].ModelCount != len(registry.GetOpenCodeGoModels()) {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestPatchOpenCodeGoKeyUpdatesProtocolConfig(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{
			{Name: "primary", APIKey: "go-key", BaseURL: config.DefaultOpenCodeGoBaseURL},
			{Name: "backup", APIKey: "go-backup", BaseURL: config.DefaultOpenCodeGoBaseURL},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/opencode-go-api-key", strings.NewReader(`{
        "index": 0,
        "value": {"name": " renamed ", "priority": 7, "disable-cooling": true, "models": [{"name":"minimax-m3","alias":"mini","protocol":"messages"}]}
    }`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenCodeGoKey(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	entry := h.cfg.OpenCodeGoKey[0]
	if entry.Name != "renamed" || entry.APIKey != "go-key" || entry.Priority != 7 || !entry.DisableCooling || len(entry.Models) != 1 || entry.Models[0].Protocol != config.OpenCodeGoProtocolClaude {
		t.Fatalf("entry = %#v", entry)
	}
	if backup := h.cfg.OpenCodeGoKey[1]; backup.Name != "backup" || backup.APIKey != "go-backup" {
		t.Fatalf("backup changed = %#v", backup)
	}
}

func TestPutOpenCodeGoKeysPersistsMultipleAccounts(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode-go-api-key", strings.NewReader(`[
        {"name":" primary ","api-key":"go-primary"},
        {"name":"backup","api-key":"go-backup","priority":2}
    ]`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenCodeGoKeys(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(h.cfg.OpenCodeGoKey) != 2 {
		t.Fatalf("accounts = %#v", h.cfg.OpenCodeGoKey)
	}
	if h.cfg.OpenCodeGoKey[0].Name != "primary" || h.cfg.OpenCodeGoKey[1].Priority != 2 {
		t.Fatalf("accounts = %#v", h.cfg.OpenCodeGoKey)
	}
}

func TestDeleteOpenCodeGoKeyRemovesOnlySelectedAccount(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{
			{Name: "primary", APIKey: "go-primary"},
			{Name: "backup", APIKey: "go-backup"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/opencode-go-api-key?index=0", nil)

	h.DeleteOpenCodeGoKey(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(h.cfg.OpenCodeGoKey) != 1 || h.cfg.OpenCodeGoKey[0].APIKey != "go-backup" {
		t.Fatalf("remaining accounts = %#v", h.cfg.OpenCodeGoKey)
	}
}

func TestPatchAuthFileStatusFindsOpenCodeGoByPublicAuthIndex(t *testing.T) {
	cfg := &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{{APIKey: "go-key"}}}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil || len(auths) != 1 {
		t.Fatalf("Synthesize() auths = %#v, error = %v", auths, err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err = manager.Register(context.Background(), auths[0]); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t), authManager: manager}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+auths[0].EnsureIndex()+`","disabled":true}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchAuthFileStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(h.cfg.OpenCodeGoKey[0].ExcludedModels) != 1 || h.cfg.OpenCodeGoKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("excluded models = %#v", h.cfg.OpenCodeGoKey[0].ExcludedModels)
	}
}
