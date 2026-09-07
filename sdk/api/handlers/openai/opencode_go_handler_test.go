package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestOpenCodeGoChatEndpointPreservesEventFramedOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/responses" || gjson.GetBytes(body, "max_output_tokens").Int() != 128 || gjson.GetBytes(body, "reasoning.effort").Exists() {
			t.Errorf("incorrect upstream request: %s %s", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_1","created_at":1}}`,
			`{"type":"response.output_text.delta","delta":"print(\"OK\")"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		} {
			_, _ = io.WriteString(w, "event: "+gjson.Get(event, "type").String()+"\ndata: "+event+"\n\n")
			w.(http.Flusher).Flush()
		}
		// The provider can keep sending pings after the terminal event.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer upstream.Close()
	m := coreauth.NewManager(nil, nil, nil)
	m.RegisterExecutor(runtimeexecutor.NewOpenCodeGoExecutor(&config.Config{}))
	a := &coreauth.Auth{ID: "opencode-http-test", Provider: "opencode-go", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	const model = "muse-spark-1.3-contributor"
	registry.GetGlobalRegistry().RegisterClient(a.ID, "opencode-go", []*registry.ModelInfo{{ID: model, SupportedEndpoints: []string{openAIResponsesEndpoint}}})
	defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m))
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}],"max_tokens":128,"stream":true}`))
	start := time.Now()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if time.Since(start) > time.Second {
		t.Fatal("waited for upstream EOF after response.completed")
	}
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"content":"print(\"OK\")"`) || !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("unusable HTTP response: %d %s", w.Code, w.Body.String())
	}
}
