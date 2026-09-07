package executor

import (
	"testing"
	"time"
)

func TestCodexQuotaScopeAcrossTransports(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		scoped     bool
	}{
		{"nested", `{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`, true},
		{"top_level", `{"type":"USAGE_LIMIT_REACHED","resets_in_seconds":123}`, true},
		{"rate_limit", `{"error":{"type":"rate_limit_error"}}`, false},
		{"capacity", `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(429, []byte(tc.body))
			if err.IsCredentialScoped() != tc.scoped {
				t.Fatal("wrong HTTP quota scope")
			}
			if tc.scoped && (err.RetryAfter() == nil || *err.RetryAfter() != 123*time.Second) {
				t.Fatal("HTTP reset was not preserved")
			}
		})
	}
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`))
	if !ok {
		t.Fatal("WS error not recognized")
	}
	scoped, ok := err.(interface {
		IsCredentialScoped() bool
		RetryAfter() *time.Duration
	})
	if !ok || !scoped.IsCredentialScoped() || scoped.RetryAfter() == nil || *scoped.RetryAfter() != 123*time.Second {
		t.Fatal("WS quota scope/reset was not preserved")
	}
}
