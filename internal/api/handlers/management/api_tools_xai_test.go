package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func xaiOAuthTestAuth(tokenEndpoint string, expired bool) *coreauth.Auth {
	now := time.Now().UTC()
	var expiredAt time.Time
	if expired {
		expiredAt = now.Add(-time.Hour)
	} else {
		expiredAt = now.Add(24 * time.Hour)
	}
	return &coreauth.Auth{
		ID:       "xai-oauth-test-id",
		Provider: "xai",
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"access_token":   "old-access-token",
			"refresh_token":  "old-refresh-token",
			"token_endpoint": tokenEndpoint,
			"expired":        expiredAt.Format(time.RFC3339),
		},
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
}

func xaiOAuthLegacyMetadataAuth(tokenEndpoint, refreshToken string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "xai-oauth-legacy-id",
		Provider: "xai",
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"access_token":   "legacy-access-token",
			"refresh_token":  refreshToken,
			"token_endpoint": tokenEndpoint,
		},
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
}

func TestXAIOAuthTokenNeedsRefresh_LegacyMetadataWithoutExpiryOrLastRefresh(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{
			"access_token":  "present-access",
			"refresh_token": "present-refresh",
		},
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	if !xaiOAuthTokenNeedsRefresh(auth) {
		t.Fatal("expected legacy metadata with access_token but no expiry/last_refresh to need refresh")
	}
}

func TestResolveTokenForAuth_XAIOAuthLegacyMetadataRefreshes(t *testing.T) {
	var refreshCalls int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-legacy-access",
			"refresh_token": "refreshed-legacy-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	auth := xaiOAuthLegacyMetadataAuth(tokenServer.URL, "legacy-refresh-token-unique-a")
	h := &Handler{cfg: &config.Config{}}

	token, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err != nil {
		t.Fatalf("resolveTokenForAuth() error = %v", err)
	}
	if token != "refreshed-legacy-access" {
		t.Fatalf("token = %q, want refreshed-legacy-access", token)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestResolveTokenForAuth_XAIOAuthExpiredRefreshes(t *testing.T) {
	var refreshCalls int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "rotated-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	auth := xaiOAuthTestAuth(tokenServer.URL, true)
	auth.Metadata["refresh_token"] = "expired-refresh-token-unique-b"
	h := &Handler{cfg: &config.Config{}}

	token, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err != nil {
		t.Fatalf("resolveTokenForAuth() error = %v", err)
	}
	if token != "new-access-token" {
		t.Fatalf("token = %q, want new-access-token", token)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if auth.Metadata["access_token"] != "new-access-token" {
		t.Fatalf("metadata access_token = %v, want new-access-token", auth.Metadata["access_token"])
	}
	if auth.Metadata["refresh_token"] != "rotated-refresh-token" {
		t.Fatalf("metadata refresh_token = %v, want rotated-refresh-token", auth.Metadata["refresh_token"])
	}
}

func TestResolveTokenForAuth_XAIOAuthStillValidSkipsRefresh(t *testing.T) {
	var refreshCalls int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	auth := xaiOAuthTestAuth(tokenServer.URL, false)
	h := &Handler{cfg: &config.Config{}}

	token, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err != nil {
		t.Fatalf("resolveTokenForAuth() error = %v", err)
	}
	if token != "old-access-token" {
		t.Fatalf("token = %q, want old-access-token", token)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
	}
}

func TestResolveTokenForAuth_XAIAPIKeySkipsRefresh(t *testing.T) {
	var refreshCalls int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	auth := &coreauth.Auth{
		ID:       "xai-apikey-test-id",
		Provider: "xai",
		Attributes: map[string]string{
			"api_key": "secret-xai-api-key",
		},
		Metadata: map[string]any{
			"token_endpoint": tokenServer.URL,
			"refresh_token":  "should-not-be-used",
		},
	}
	h := &Handler{cfg: &config.Config{}}

	token, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err != nil {
		t.Fatalf("resolveTokenForAuth() error = %v", err)
	}
	if token != "secret-xai-api-key" {
		t.Fatalf("token = %q, want secret-xai-api-key", token)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
	}
}

func TestResolveTokenForAuth_XAIRefreshErrorDoesNotLeakSecret(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer tokenServer.Close()

	auth := xaiOAuthTestAuth(tokenServer.URL, true)
	auth.Metadata["refresh_token"] = "error-refresh-token-unique-c"
	h := &Handler{cfg: &config.Config{}}

	_, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "error-refresh-token-unique-c") {
		t.Fatalf("error leaked refresh token: %q", errMsg)
	}
	if strings.Contains(errMsg, "old-access-token") {
		t.Fatalf("error leaked access token: %q", errMsg)
	}
}

func TestResolveTokenForAuth_XAIRetainsRefreshTokenWhenUpstreamOmitsIt(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-only",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	auth := xaiOAuthTestAuth(tokenServer.URL, true)
	auth.Metadata["refresh_token"] = "retain-refresh-token-unique-d"
	h := &Handler{cfg: &config.Config{}}

	token, err := h.resolveTokenForAuth(context.Background(), auth, "")
	if err != nil {
		t.Fatalf("resolveTokenForAuth() error = %v", err)
	}
	if token != "new-access-only" {
		t.Fatalf("token = %q, want new-access-only", token)
	}
	if auth.Metadata["refresh_token"] != "retain-refresh-token-unique-d" {
		t.Fatalf("refresh_token = %v, want retain-refresh-token-unique-d preserved", auth.Metadata["refresh_token"])
	}
}

func TestAPICall_XAIOAuthEndToEndRefreshesAndForwardsBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var billingAuth string
	billingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		billingAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer billingServer.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "billing-access-token",
			"refresh_token": "persisted-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	manager := coreauth.NewManager(&memoryAuthStore{items: make(map[string]*coreauth.Auth)}, nil, nil)
	auth := xaiOAuthTestAuth(tokenServer.URL, true)
	auth.Metadata["refresh_token"] = "e2e-refresh-token-unique-e"
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	body := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + billingServer.URL + `","header":{"Authorization":"Bearer $TOKEN$"}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("APICall status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if billingAuth != "Bearer billing-access-token" {
		t.Fatalf("billing Authorization = %q, want Bearer billing-access-token", billingAuth)
	}

	respBody := rec.Body.String()
	if strings.Contains(respBody, "persisted-refresh-token") || strings.Contains(respBody, "e2e-refresh-token-unique-e") {
		t.Fatalf("APICall response leaked refresh token: %s", respBody)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	if updated.Metadata["access_token"] != "billing-access-token" {
		t.Fatalf("persisted access_token = %v, want billing-access-token", updated.Metadata["access_token"])
	}
	if updated.Metadata["refresh_token"] != "persisted-refresh-token" {
		t.Fatalf("persisted refresh_token = %v, want persisted-refresh-token", updated.Metadata["refresh_token"])
	}
}
