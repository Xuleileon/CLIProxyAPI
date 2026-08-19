package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestGrokImage20GenerationEndToEnd(t *testing.T) {
	var upstreamPath string
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		var errRead error
		upstreamBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream request: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	manager := server.handlers.AuthManager
	manager.RegisterExecutor(runtimeexecutor.NewXAIExecutor(server.cfg))
	const authID = "xai-grok-image-20-e2e"
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":  upstream.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "test-xai-token"},
	})
	if errRegister != nil {
		t.Fatalf("register xAI auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "xai", registry.GetXAIModels())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-imagine-image-2.0","prompt":"draw a fox","response_format":"b64_json"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if upstreamPath != "/images/generations" {
		t.Fatalf("upstream path = %q, want /images/generations", upstreamPath)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "grok-imagine-image-2.0" {
		t.Fatalf("upstream model = %q, body = %s", got, upstreamBody)
	}
	if got := gjson.Get(recorder.Body.String(), "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("response image = %q, body = %s", got, recorder.Body.String())
	}
}
