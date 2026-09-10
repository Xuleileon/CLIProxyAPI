package test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// These tests exercise the public handlers, scheduler, alias resolution and executor together.
func TestCommandCodePublicProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct{ path, body, terminal string }{
		{"/v1/chat/completions", `{"model":"cc/flash","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high","stream":true}`, "data: [DONE]"},
		{"/v1/messages", `{"model":"cc/flash","messages":[{"role":"user","content":"hello"}],"max_tokens":128,"stream":true}`, "event: message_stop"},
		{"/v1/responses", `{"model":"cc/flash","input":"hello","max_output_tokens":128,"stream":true}`, "event: response.completed"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(body, "params.model").String() != "deepseek/deepseek-v4-flash" {
					t.Errorf("alias not resolved: %s", body)
				}
				id, err := uuid.Parse(gjson.GetBytes(body, "threadId").String())
				if err != nil || id.Variant() != uuid.RFC4122 {
					t.Error("invalid upstream session UUID")
				}
				if tc.path == "/v1/chat/completions" && gjson.GetBytes(body, "params.reasoning_effort").String() != "high" {
					t.Error("reasoning intent lost")
				}
				_, _ = io.WriteString(w, "{\"type\":\"text-delta\",\"text\":\"PUBLIC_OK\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\",\"totalUsage\":{\"inputTokens\":12,\"outputTokens\":3}}\n")
			}))
			defer server.Close()
			cfg := &config.Config{CommandCodeKey: []config.CommandCodeKey{{APIKey: "test", BaseURL: server.URL, Prefix: "cc", Models: []config.CommandCodeModel{{Name: "deepseek/deepseek-v4-flash", Alias: "flash"}}}}}
			manager := auth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCommandCodeExecutor(cfg))
			a := &auth.Auth{ID: "command-code-http-" + uuid.NewString(), Provider: "command-code", Prefix: "cc", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			if _, err := manager.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, "command-code", []*registry.ModelInfo{{ID: "cc/flash", UserDefined: true, SupportedEndpoints: []string{"/chat/completions", "/messages", "/responses"}}})
			defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
			base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
			router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
			router.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("x-session-id", "client-session-not-uuid")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "PUBLIC_OK") || !strings.Contains(recorder.Body.String(), tc.terminal) {
				t.Fatalf("unusable HTTP response: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
