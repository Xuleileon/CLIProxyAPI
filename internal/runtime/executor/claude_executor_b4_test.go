package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// --- B4-Claude: streaming headers (c405398a) ---

func TestClaudeExecutor_Execute_UpstreamStreamDecision(t *testing.T) {
	claudeJSON := `{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	claudeSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":1}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	tests := []struct {
		name           string
		sourceFormat   string
		responseFormat string // empty means same as source
		wantStream     bool
		wantAccept     string
		wantEncoding   string
		upstreamBody   string
	}{
		{
			name:         "native claude to claude uses JSON path",
			sourceFormat: "claude",
			wantStream:   false,
			wantAccept:   "application/json",
			wantEncoding: "gzip, deflate, br, zstd",
			upstreamBody: claudeJSON,
		},
		{
			name:         "openai to claude uses upstream SSE",
			sourceFormat: "openai",
			wantStream:   true,
			wantAccept:   "text/event-stream",
			wantEncoding: "identity",
			upstreamBody: claudeSSE,
		},
		{
			name:           "openai source with explicit claude response format uses JSON",
			sourceFormat:   "openai",
			responseFormat: "claude",
			wantStream:     false,
			wantAccept:     "application/json",
			wantEncoding:   "gzip, deflate, br, zstd",
			upstreamBody:   claudeJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAccept, gotEncoding string
			var gotBodyStream interface{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAccept = r.Header.Get("Accept")
				gotEncoding = r.Header.Get("Accept-Encoding")
				raw, _ := io.ReadAll(r.Body)
				gotBodyStream = gjson.GetBytes(raw, "stream").Value()

				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tt.upstreamBody))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}
			payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`)

			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tt.sourceFormat),
			}
			if tt.responseFormat != "" {
				opts.ResponseFormat = sdktranslator.FromString(tt.responseFormat)
			}

			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-3-5-sonnet-20241022",
				Payload: payload,
			}, opts)
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			if gotAccept != tt.wantAccept {
				t.Errorf("Accept = %q, want %q", gotAccept, tt.wantAccept)
			}
			if gotEncoding != tt.wantEncoding {
				t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, tt.wantEncoding)
			}
			if gotBodyStream != tt.wantStream {
				t.Errorf("body stream = %v (%T), want %v", gotBodyStream, gotBodyStream, tt.wantStream)
			}
		})
	}
}

func TestClaudeExecutor_Execute_StreamBoolOverridesClientValue(t *testing.T) {
	var gotStreamRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotStreamRaw = gjson.GetBytes(raw, "stream").Raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	// Client sends stream:true but native claude→claude must force false
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotStreamRaw != "false" {
		t.Errorf("body stream = %s, want false (client stream:true must be overridden)", gotStreamRaw)
	}
}

// --- B4-Claude: OAuth SSE tool-name restore in Execute (08e55157) ---

func TestClaudeExecutor_Execute_OAuthSSEToolNameRestore(t *testing.T) {
	// OAuth token triggers tool-name remapping: "bash" -> "Bash" upstream.
	// The upstream SSE response contains "Bash" which must be restored to "bash".
	// SourceFormat=claude + ResponseFormat=openai forces upstreamStream=true.
	sseBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022"}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var gotUpstreamToolName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotUpstreamToolName = gjson.GetBytes(raw, "tools.0.name").String()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "sk-ant-oat-test-token",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"run ls"}],"tools":[{"name":"bash","description":"run commands","input_schema":{"type":"object"}}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("claude"),
		ResponseFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Upstream should have received TitleCase "Bash"
	if gotUpstreamToolName != "Bash" {
		t.Errorf("upstream tool name = %q, want %q", gotUpstreamToolName, "Bash")
	}

	// Response is translated to OpenAI format; tool name should be restored to "bash"
	gotName := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String()
	if gotName != "bash" {
		t.Errorf("response tool name = %q, want %q; payload=%s", gotName, "bash", string(resp.Payload))
	}
}

func TestClaudeExecutor_Execute_NonOAuthDoesNotRemapToolNames(t *testing.T) {
	sseBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022"}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var gotUpstreamToolName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotUpstreamToolName = gjson.GetBytes(raw, "tools.0.name").String()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "sk-ant-api-key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"run ls"}],"tools":[{"name":"bash","description":"run commands","input_schema":{"type":"object"}}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("claude"),
		ResponseFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Non-OAuth: tool name should pass through unchanged
	if gotUpstreamToolName != "bash" {
		t.Errorf("upstream tool name = %q, want %q (non-OAuth should not remap)", gotUpstreamToolName, "bash")
	}
}

func TestClaudeExecutor_Execute_NonStreamJSONResponseToolNameRestore(t *testing.T) {
	// Non-stream (JSON) response with OAuth should still restore tool names
	// via restoreClaudeOAuthToolNamesFromResponse.
	jsonResp := `{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":5,"output_tokens":3}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jsonResp))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "sk-ant-oat-test-token",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"run ls"}],"tools":[{"name":"bash","description":"run commands","input_schema":{"type":"object"}}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Non-stream JSON response should have tool name restored from "Bash" to "bash"
	gotName := gjson.GetBytes(resp.Payload, "content.0.name").String()
	if gotName != "bash" {
		t.Errorf("response tool name = %q, want %q", gotName, "bash")
	}
}

// TestClaudeExecutor_Execute_AcceptEncodingNotOverriddenByCustomAttrs verifies
// that custom header attributes cannot bypass the SSE transport contract.
func TestClaudeExecutor_Execute_AcceptEncodingNotOverriddenByCustomAttrs(t *testing.T) {
	var gotAccept, gotEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022"}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":1}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                "key-123",
		"base_url":               server.URL,
		"header_Accept":          "application/json",
		"header_Accept-Encoding": "gzip",
	}}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Custom attrs must be overridden for SSE streams
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want %q (custom attr must not override SSE contract)", gotAccept, "text/event-stream")
	}
	if gotEncoding != "identity" {
		t.Errorf("Accept-Encoding = %q, want %q (custom attr must not override SSE contract)", gotEncoding, "identity")
	}
}
