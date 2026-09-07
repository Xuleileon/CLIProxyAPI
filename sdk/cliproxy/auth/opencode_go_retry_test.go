package auth

import (
	"context"
	"testing"
	"time"
)

func TestOpenCodeGoShortRetryCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	previousTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })
	for _, provider := range []string{"opencode-go", "codex"} {
		for _, model := range []string{"", "muse-test"} {
			for _, status := range []int{429, 502} {
				m := NewManager(nil, nil, nil)
				a := &Auth{ID: "retry-test", Provider: provider}
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				delay := 2 * time.Second
				start := time.Now()
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: provider, Model: model, Error: &Error{HTTPStatus: status, Message: "EOF"}, RetryAfter: &delay})
				got, _ := m.GetByID(a.ID)
				next := got.NextRetryAfter
				if model != "" {
					next = got.ModelStates[model].NextRetryAfter
				}
				want := 2 * time.Second
				if provider != "opencode-go" {
					want = 5 * time.Second
					if status == 429 {
						want = 10 * time.Second
					}
				}
				if next.Sub(start) < want || next.Sub(start) > want+time.Second {
					t.Fatalf("provider=%s model=%q status=%d cooldown=%v want=%v", provider, model, status, next.Sub(start), want)
				}
			}
		}
	}
}
