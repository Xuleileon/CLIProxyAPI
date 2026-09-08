package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenCodeGoRetriesBeforeOutput(t *testing.T) {
	for _, failure := range []string{"429", "submitted EOF", "stream error", "after output", "long retry", "disabled"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					switch failure {
					case "429", "long retry", "disabled":
						if failure == "long retry" {
							w.Header().Set("Retry-After", "120")
						}
						w.WriteHeader(429)
						_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"Please retry after a brief wait"}}`)
						return
					case "submitted EOF":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					case "stream error", "after output":
						w.Header().Set("Content-Type", "text/event-stream")
						if failure == "after output" {
							_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
						}
						_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed\"}}}\n\n")
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer server.Close()
			cfg := &config.Config{}
			m := cliproxyauth.NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			m.SetRetryConfig(1, 30*time.Second, 0)
			if failure == "disabled" {
				m.SetRetryConfig(0, 30*time.Second, 0)
			}
			m.RegisterExecutor(NewOpenCodeGoExecutor(cfg))
			a := openCodeGoTestAuth(server.URL + "/v1")
			a.ID = "opencode-retry-" + failure
			const model = "muse-spark-1.3-contributor"
			registry.GetGlobalRegistry().RegisterClient(a.ID, "opencode-go", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			const payload = `{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}],"stream":true}`
			start := time.Now()
			result, err := m.ExecuteStream(context.Background(), []string{"opencode-go"}, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(payload)})
			var output strings.Builder
			if result != nil {
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						err = chunk.Err
					}
					output.WriteString(gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String())
				}
			}
			if failure == "long retry" || failure == "disabled" || failure == "after output" || failure == "submitted EOF" {
				if calls.Load() != 1 || err == nil {
					t.Fatalf("unexpected replay/success: calls=%d err=%v", calls.Load(), err)
				}
				if failure == "after output" && output.String() != "partial" {
					t.Fatalf("output lost: %q", output.String())
				}
				return
			}
			if err != nil || calls.Load() != 2 || output.String() != "OK" {
				t.Fatalf("calls=%d output=%q err=%v", calls.Load(), output.String(), err)
			}
			if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 5*time.Second {
				t.Fatalf("retry took %v", elapsed)
			}
		})
	}
}
