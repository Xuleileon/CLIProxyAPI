package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestModelCooldownSafeRetryAfter(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", newModelCooldownError("model", "codex", 1500*time.Millisecond))
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After=%q, want 2", got)
	}
}
