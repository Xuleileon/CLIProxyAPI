package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenCodeGoCompactedHistory(t *testing.T) {
	for _, model := range []string{"muse-spark-1.3-contributor", "deepseek-v4-flash", "minimax-m3"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "Remember MAPLE_72") || strings.Contains(string(body), `"type":"compaction"`) {
					t.Errorf("compacted summary lost or untranslated: %s", body)
				}
				if !strings.Contains(string(body), "9007199254740993") {
					t.Errorf("tool argument precision lost: %s", body)
				}
				// Stop after capturing the translated request; no response translation needed.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"captured"}}`)
			}))
			defer server.Close()
			payload := []byte(`{"model":"` + model + `","max_tokens":512,"messages":[{"role":"assistant","content":[{"type":"compaction","content":"Remember MAPLE_72","signature":"anthropic-only"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"echo","input":{"id":9007199254740993}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}]}`)
			e := NewOpenCodeGoExecutor(&config.Config{})
			_, err := e.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
			if err == nil {
				t.Fatal("expected capture status")
			}
		})
	}
}

func TestOpenCodeGoCompactUsesDedicatedEndpoint(t *testing.T) {
	for _, model := range []string{"muse-spark-1.3-contributor", "deepseek-v4-flash", "minimax-m3"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1/responses/compact" || gjson.GetBytes(body, "stream").Exists() {
					t.Errorf("compaction became generation: path=%s body=%s", r.URL.Path, body)
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"message":"unsupported compact"}}`)
			}))
			defer server.Close()
			e := NewOpenCodeGoExecutor(&config.Config{})
			_, err := e.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"summary","stream":false}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
			var status cliproxyexecutor.StatusError
			if !errors.As(err, &status) || status.StatusCode() != http.StatusNotFound {
				t.Fatalf("unsupported compaction hidden: %v", err)
			}
		})
	}
}

func TestOpenCodeGoStreamRejectsPrematureEOF(t *testing.T) {
	for _, model := range []string{"muse-spark-1.3-contributor", "minimax-m3"} {
		for _, target := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
			t.Run(model+"/"+target.String(), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if r.URL.Path == "/v1/messages" {
						_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1}}}\n\n")
					} else {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					}
				}))
				defer server.Close()
				e := NewOpenCodeGoExecutor(&config.Config{})
				resp, err := e.ExecuteStream(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":128}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: target})
				if err != nil {
					t.Fatal(err)
				}
				var lastErr error
				for chunk := range resp.Chunks {
					if chunk.Err != nil {
						lastErr = chunk.Err
					}
				}
				if !errors.Is(lastErr, io.ErrUnexpectedEOF) {
					t.Fatalf("premature EOF reported as success: %v", lastErr)
				}
			})
		}
	}
}

func TestOpenCodeGoAnthropicPreservesOpaqueToolData(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"verify","id":"c1","cache_control":{"type":"ephemeral"},"input":{"signature":"business-signature","cache_control":"business-cache","items":[{"type":"thinking","id":9007199254740993}]}},{"type":"thinking","thinking":"private","signature":"provider-signature"}]}],"tools":[{"name":"verify","cache_control":{"type":"ephemeral"},"input_schema":{"type":"object","properties":{"signature":{"type":"string"},"cache_control":{"type":"string"}}}}]}`)
	out := sanitizeOpenCodeGoAnthropicPayload(payload)
	for path, want := range map[string]string{"messages.0.content.0.input.signature": "business-signature", "messages.0.content.0.input.cache_control": "business-cache", "messages.0.content.0.input.items.0.id": "9007199254740993", "tools.0.input_schema.properties.signature.type": "string"} {
		if gjson.GetBytes(out, path).String() != want {
			t.Errorf("business data changed at %s: %s", path, out)
		}
	}
	if gjson.GetBytes(out, "messages.0.content.#").Int() != 1 || gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists() || gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("protocol extensions not removed: %s", out)
	}
}
