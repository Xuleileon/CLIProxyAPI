package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenCodeGoSessionOnWire(t *testing.T) {
	protocols := []struct {
		model, path, first, later string
		format                    sdktranslator.Format
	}{
		{"deepseek-v4-flash", "/chat/completions", `{"messages":[{"role":"user","content":"first"}]}`, `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`, sdktranslator.FormatOpenAI},
		{"minimax-m3", "/messages", `{"messages":[{"role":"user","content":"first"}]}`, `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`, sdktranslator.FormatClaude},
		{"gpt-5.6-luna", "/responses", `{"input":"first"}`, `{"input":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`, sdktranslator.FormatOpenAIResponse},
	}
	for _, protocol := range protocols {
		for _, stream := range []bool{false, true} {
			name := protocol.model
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				var headers []http.Header
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1"+protocol.path {
						t.Errorf("path = %s", r.URL.Path)
					}
					headers = append(headers, r.Header.Clone())
					if len(headers) == 1 {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, `{"error":{"message":"retry"}}`)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						switch protocol.format {
						case sdktranslator.FormatClaude:
							_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
						case sdktranslator.FormatOpenAIResponse:
							_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
						default:
							_, _ = io.WriteString(w, "data: [DONE]\n\n")
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{}`)
					}
				}))
				defer server.Close()
				executor := NewOpenCodeGoExecutor(&config.Config{})
				auth := openCodeGoTestAuth(server.URL + "/v1")
				opts := cliproxyexecutor.Options{SourceFormat: protocol.format, ResponseFormat: protocol.format, Stream: stream}
				call := func(payload string, wantError bool) {
					t.Helper()
					req := cliproxyexecutor.Request{Model: protocol.model, Payload: []byte(payload)}
					var err error
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, err = executor.ExecuteStream(context.Background(), auth, req, opts)
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
							}
						}
					} else {
						_, err = executor.Execute(context.Background(), auth, req, opts)
					}
					if (err != nil) != wantError {
						t.Fatalf("error = %v, wantError = %v", err, wantError)
					}
				}
				call(protocol.first, true)
				call(protocol.first, false)
				call(protocol.later, false)
				call(strings.ReplaceAll(protocol.first, "first", "another conversation"), false)
				id := headers[0].Get("X-Opencode-Session")
				if id == "" || id != headers[1].Get("X-Opencode-Session") || id != headers[2].Get("X-Opencode-Session") || id == headers[3].Get("X-Opencode-Session") {
					t.Fatal("missing or unstable session across retry/continuation, or separate roots collided")
				}
				// Explicit caller IDs and dynamic configuration work on every transport.
				opts.Headers = http.Header{"x-opencode-session": {"caller-session"}, "X-Test-Session": {"configured-session"}}
				call(protocol.first, false)
				if got := headers[4].Get("X-Opencode-Session"); got != "caller-session" {
					t.Fatalf("client session = %q", got)
				}
				auth.Attributes["header:x-opencode-session"] = "$X-Test-Session"
				call(protocol.first, false)
				if got := headers[5].Get("X-Opencode-Session"); got != "configured-session" {
					t.Fatalf("configured session = %q", got)
				}
				for _, h := range headers {
					if h.Get("User-Agent") != "GoSubscriptionClient/1.0" {
						t.Errorf("unexpected adapter user agent: %q", h.Get("User-Agent"))
					}
					if h.Get("X-Opencode-Client") != "" || h.Get("X-Opencode-Project") != "" {
						t.Fatal("unexpected client/project attribution")
					}
				}
				auth.Attributes["header:User-Agent"] = "custom-client"
				call(protocol.first, false)
				if headers[6].Get("User-Agent") != "custom-client" {
					t.Fatal("configured user agent was replaced")
				}
			})
		}
	}
}

func TestOpenCodeGoRawRequestPreservesBody(t *testing.T) {
	const payload = `{"input":"raw request"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != payload || r.Header.Get("X-Opencode-Session") == "" {
			t.Error("raw request lost its body or session header")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	// A non-replayable reader exercises the SDK path without GetBody.
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", io.NopCloser(strings.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewOpenCodeGoExecutor(&config.Config{}).HttpRequest(context.Background(), openCodeGoTestAuth(server.URL), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Body.Close()
	if req.Header.Get("X-Opencode-Session") != "" {
		t.Fatal("raw caller headers were mutated")
	}
}

func TestOpenCodeGoSessionDoesNotLeakToOtherProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Opencode-Session") != "" {
			t.Error("OpenCode header leaked to another provider")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	_, err := NewOpenAICompatExecutor("other", &config.Config{}).Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: "other", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: http.Header{"X-Opencode-Session": {"client-session"}}})
	if err != nil {
		t.Fatal(err)
	}
}
