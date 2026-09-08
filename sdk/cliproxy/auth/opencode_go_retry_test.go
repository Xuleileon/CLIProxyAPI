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

func TestOpenCodeGoCapacityBackoffSharesWindow(t *testing.T) {
	now := time.Now()
	var quota QuotaState
	for _, want := range []time.Duration{2, 4, 8, 16, 30, 30} {
		next, level := openCodeGoCapacityCooldown(quota, now)
		if next.Sub(now) != want*time.Second {
			t.Fatalf("delay=%v want=%vs", next.Sub(now), want)
		}
		quota = QuotaState{NextRecoverAt: next, BackoffLevel: level}
		shared, sharedLevel := openCodeGoCapacityCooldown(quota, now.Add(time.Millisecond))
		if shared != next || sharedLevel != level {
			t.Fatal("concurrent failure escalated the open window")
		}
		now = next.Add(time.Millisecond)
	}
}

func TestOpenCodeGoAuthoritativeRetryAndCapacity(t *testing.T) {
	for _, model := range []string{"", "muse-test"} {
		for _, tc := range []struct {
			name    string
			status  int
			message string
			hint    *time.Duration
			want    time.Duration
		}{
			{"capacity", 429, "rate_limit_exceeded", nil, 2 * time.Second},
			{"server hint", 429, "rate_limit_exceeded", new(250 * time.Millisecond), 250 * time.Millisecond},
			{"safe reconnect", 502, "EOF", new(250 * time.Millisecond), 250 * time.Millisecond},
		} {
			m := NewManager(nil, nil, nil)
			a := &Auth{ID: "opencode-window", Provider: "opencode-go"}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: tc.status, Message: tc.message}, RetryAfter: tc.hint})
			got, _ := m.GetByID(a.ID)
			next := got.NextRetryAfter
			if model != "" {
				next = got.ModelStates[model].NextRetryAfter
			}
			if next.Sub(start) < tc.want || next.Sub(start) > tc.want+time.Second {
				t.Fatalf("%s model=%q wait=%v", tc.name, model, next.Sub(start))
			}
		}
	}
	for _, message := range []string{"GoUsageLimitError", "FreeUsageLimitError"} {
		if isOpenCodeGoCapacityError(&Auth{Provider: "opencode-go"}, &Error{HTTPStatus: 429, Message: message}) {
			t.Fatal("subscription exhaustion treated as capacity")
		}
	}
}
