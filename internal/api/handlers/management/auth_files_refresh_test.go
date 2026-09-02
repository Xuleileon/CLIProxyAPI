package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type authRefreshTestExecutor struct {
	calls int
	err   error
}

func (e *authRefreshTestExecutor) Identifier() string { return "claude" }

func (e *authRefreshTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *authRefreshTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *authRefreshTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed"
	return auth, nil
}

func (e *authRefreshTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *authRefreshTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestRefreshAuthFileWaitsForSuccessfulRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &authRefreshTestExecutor{}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		FileName: "claude-auth.json",
		Provider: "claude",
		Metadata: map[string]any{"refresh_token": "present"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	router := gin.New()
	router.POST("/v0/management/auth-files/refresh", handler.RefreshAuthFile)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/refresh", strings.NewReader(`{"name":"claude-auth.json"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", executor.calls)
	}
	updated, ok := manager.GetByID("claude-auth")
	if !ok || updated == nil || updated.LastRefreshedAt.IsZero() {
		t.Fatalf("expected persisted refresh timestamp, auth = %+v", updated)
	}
	if !strings.Contains(recorder.Body.String(), `"last_refresh"`) {
		t.Fatalf("response does not expose refresh completion: %s", recorder.Body.String())
	}
}

func TestRefreshAuthFileReturnsUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &authRefreshTestExecutor{err: errors.New("refresh rejected")}
	manager.RegisterExecutor(executor)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "claude-auth", FileName: "claude-auth.json", Provider: "claude"})

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/refresh", strings.NewReader(`{"name":"claude-auth.json"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.RefreshAuthFile(ctx)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
}

func TestAuthFileReadsDisableBrowserCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, coreauth.NewManager(nil, nil, nil))

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	handler.ListAuthFiles(listContext)
	if got := listRecorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("auth file list Cache-Control = %q, want no-store", got)
	}

	modelsRecorder := httptest.NewRecorder()
	modelsContext, _ := gin.CreateTestContext(modelsRecorder)
	modelsContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name=missing", nil)
	handler.GetAuthFileModels(modelsContext)
	if got := modelsRecorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("auth file models Cache-Control = %q, want no-store", got)
	}
}
