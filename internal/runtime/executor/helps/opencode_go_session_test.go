package helps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenCodeGoSessionIdentity(t *testing.T) {
	first := `{"messages":[{"role":"user","content":"private prompt"}]}`
	later := `{"messages":[{"role":"user","content":"private prompt"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`
	tests := []struct {
		name     string
		headers  http.Header
		metadata map[string]any
		first    string
		later    string
	}{
		{name: "derived tool loop", first: first, later: later},
		{name: "claude", headers: http.Header{"X-Claude-Code-Session-Id": {"private-session"}}, first: first, later: later},
		{name: "codex", headers: http.Header{"Session_id": {"private-session"}}, first: first, later: later},
		{name: "cache key", first: `{"prompt_cache_key":"private-session","input":"hello"}`, later: `{"prompt_cache_key":"private-session","input":"compacted history"}`},
		{name: "execution", metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "private-execution"}, first: first, later: `{"messages":[{"role":"user","content":"compacted history"}]}`},
		{name: "identity free retry", first: `{}`, later: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			get := func(payload, scope string) string {
				req := cliproxyexecutor.Request{Payload: []byte(payload), Metadata: tt.metadata}
				opts := cliproxyexecutor.Options{Headers: tt.headers, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
				out := httptest.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
				ApplyOpenCodeGoSessionHeader(out, req, opts)
				return out.Header.Get("X-Opencode-Session")
			}
			id := get(tt.first, "caller-a")
			if _, err := uuid.Parse(id); err != nil || strings.Contains(id, "private") {
				t.Fatalf("expected opaque ID, got %q", id)
			}
			if id != get(tt.later, "caller-a") {
				t.Fatal("session changed during continuation/retry")
			}
			if id == get(tt.first, "caller-b") {
				t.Fatal("caller scopes collided")
			}
		})
	}
}

func TestOpenCodeGoSessionUsesOriginalRequest(t *testing.T) {
	out := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", nil)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"original"}]}`)}
	req := cliproxyexecutor.Request{Payload: []byte(`{"input":"translated"}`)}
	ApplyOpenCodeGoSessionHeader(out, req, opts)
	id := out.Header.Get("X-Opencode-Session")
	out.Header.Del("X-Opencode-Session")
	req.Payload = []byte(`{"input":"different translated payload"}`)
	ApplyOpenCodeGoSessionHeader(out, req, opts)
	if id != out.Header.Get("X-Opencode-Session") {
		t.Fatal("translation changed the conversation ID")
	}
}

func TestOpenCodeGoSessionHeaderPrecedence(t *testing.T) {
	for _, configured := range []string{"configured", "", "invalid\nvalue"} {
		out := httptest.NewRequest(http.MethodPost, "https://example.test", nil)
		out.Header.Set("X-Opencode-Session", configured)
		opts := cliproxyexecutor.Options{Headers: http.Header{"x-opencode-session": {"client"}}}
		ApplyOpenCodeGoSessionHeader(out, cliproxyexecutor.Request{}, opts)
		want := "client"
		if configured == "configured" {
			want = configured
		}
		if got := out.Header.Get("X-Opencode-Session"); got != want {
			t.Fatalf("session = %q, want %q", got, want)
		}
	}
}
