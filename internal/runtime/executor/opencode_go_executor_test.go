package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenCodeGoExecutorRoutesNativeProtocols(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		format         sdktranslator.Format
		payload        string
		wantPath       string
		response       string
		wantAnthropic  bool
		assertUpstream func(*testing.T, []byte)
	}{
		{
			name:     "chat completions",
			model:    "deepseek-v4-flash",
			format:   sdktranslator.FormatOpenAI,
			payload:  `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`,
			wantPath: "/v1/chat/completions",
			response: `{"id":"chat_1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		},
		{
			name:          "anthropic messages",
			model:         "minimax-m3",
			format:        sdktranslator.FormatClaude,
			payload:       `{"model":"minimax-m3","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":32},"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"thinking","thinking":"private","signature":"secret"}]}]}`,
			wantPath:      "/v1/messages",
			wantAnthropic: true,
			response:      `{"id":"msg_1","type":"message","role":"assistant","model":"minimax-m3","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
			assertUpstream: func(t *testing.T, body []byte) {
				t.Helper()
				for _, forbidden := range []string{"thinking", "cache_control", "signature"} {
					if bytes.Contains(body, []byte(forbidden)) {
						t.Fatalf("upstream Anthropic payload still contains %q: %s", forbidden, body)
					}
				}
				var root map[string]any
				if err := json.Unmarshal(body, &root); err != nil {
					t.Fatalf("invalid upstream body: %v", err)
				}
				if root["system"] != "rules" {
					t.Fatalf("system = %#v, want rules", root["system"])
				}
			},
		},
		{
			name:     "responses",
			model:    "gpt-5.6-luna",
			format:   sdktranslator.FormatOpenAIResponse,
			payload:  `{"model":"gpt-5.6-luna","input":"hi"}`,
			wantPath: "/v1/responses",
			response: `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-5.6-luna","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			assertUpstream: func(t *testing.T, body []byte) {
				t.Helper()
				if !bytes.Contains(body, []byte(`"stream":false`)) {
					t.Fatalf("Responses request was not kept non-streaming: %s", body)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.wantPath)
				}
				if r.Header.Get("Authorization") != "Bearer go-secret" {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				if tt.wantAnthropic {
					if r.Header.Get("x-api-key") != "go-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Errorf("Anthropic headers missing: %v", r.Header)
					}
				}
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()

			auth := openCodeGoTestAuth(server.URL + "/v1")
			executor := NewOpenCodeGoExecutor(&config.Config{})
			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: tt.model, Payload: []byte(tt.payload)}, cliproxyexecutor.Options{SourceFormat: tt.format, ResponseFormat: tt.format})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !bytes.Contains(resp.Payload, []byte("ok")) {
				t.Fatalf("response payload = %s", resp.Payload)
			}
			if tt.assertUpstream != nil {
				tt.assertUpstream(t, gotBody)
			}
		})
	}
}

func TestOpenCodeGoExecutorStreamsNativeProtocols(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		format   sdktranslator.Format
		payload  string
		wantPath string
		events   string
	}{
		{
			name:     "anthropic",
			model:    "qwen3.8-max",
			format:   sdktranslator.FormatClaude,
			payload:  `{"model":"qwen3.8-max","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			wantPath: "/v1/messages",
			events:   "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"qwen3.8-max\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name:     "responses",
			model:    "gpt-5.6-luna",
			format:   sdktranslator.FormatOpenAIResponse,
			payload:  `{"model":"gpt-5.6-luna","input":"hi","stream":true}`,
			wantPath: "/v1/responses",
			events:   "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5.6-luna\",\"output\":[]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.6-luna\",\"output\":[]}}\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.wantPath)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.events)
			}))
			defer server.Close()
			executor := NewOpenCodeGoExecutor(&config.Config{})
			result, err := executor.ExecuteStream(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: tt.model, Payload: []byte(tt.payload)}, cliproxyexecutor.Options{Stream: true, SourceFormat: tt.format, ResponseFormat: tt.format})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error = %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), "completed") && !strings.Contains(output.String(), "message_stop") {
				t.Fatalf("stream output = %q", output.String())
			}
		})
	}
}

func TestOpenCodeGoExecutorConfigProtocolOverride(t *testing.T) {
	cfg := &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{{
		APIKey:  "go-secret",
		BaseURL: "https://example.invalid/v1",
		Models:  []config.OpenCodeGoModel{{Name: "future-special", Alias: "future", Protocol: "anthropic"}},
	}}}
	executor := NewOpenCodeGoExecutor(cfg)
	auth := openCodeGoTestAuth("https://example.invalid/v1")
	if got := executor.protocolFor(auth, "future-special"); got != config.OpenCodeGoProtocolClaude {
		t.Fatalf("protocol = %q, want %q", got, config.OpenCodeGoProtocolClaude)
	}
	if got := executor.RequestToFormat(cliproxyexecutor.Request{Model: "future-special"}, cliproxyexecutor.Options{}); got != sdktranslator.FormatClaude {
		t.Fatalf("request format = %q, want %q", got, sdktranslator.FormatClaude)
	}
}

func TestOpenCodeGoExecutorPropagatesRateLimitStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"quota exceeded"}}`)
	}))
	defer server.Close()
	executor := NewOpenCodeGoExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: "gpt-5.6-luna", Payload: []byte(`{"model":"gpt-5.6-luna","input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err == nil {
		t.Fatal("Execute() error = nil, want rate limit error")
	}
	status, ok := err.(cliproxyexecutor.StatusError)
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %T %v, want StatusError(429)", err, err)
	}
}

func openCodeGoTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "opencode-go:test",
		Provider: "opencode-go",
		Attributes: map[string]string{
			"source":   "config:opencode-go[test]",
			"api_key":  "go-secret",
			"base_url": baseURL,
		},
	}
}
