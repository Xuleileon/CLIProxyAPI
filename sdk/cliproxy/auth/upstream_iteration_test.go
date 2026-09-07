package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type interleavedAuthExecutor struct {
	schedulerProviderTestExecutor
	change func(*Auth) (*Auth, error)
}

func (e interleavedAuthExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return e.change(a)
}
func (e interleavedAuthExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }
func (e interleavedAuthExecutor) PrepareRequestAuth(_ context.Context, a *Auth) (*Auth, error) {
	return e.change(a)
}

// Run the operator mutation after the executor has captured its input, before it returns.
func TestAuthRefreshAndPreparationPreserveInterleavedChanges(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		name := "refresh"
		if prepare {
			name = "prepare"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := &requestPrepareStore{}
			m := NewManager(store, nil, nil)
			base, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive,
				Metadata:   map[string]any{"access_token": "old", "note": "old", "remove": true},
				Attributes: map[string]string{"priority": "1"}})
			if err != nil {
				t.Fatal(err)
			}
			exec := interleavedAuthExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
			exec.change = func(a *Auth) (*Auth, error) {
				current, _ := m.GetByID(base.ID)
				current.ProxyURL = "http://new.proxy:8080"
				current.Metadata["note"] = "operator"
				delete(current.Metadata, "remove")
				current.Attributes["priority"] = "9"
				current.Disabled, current.Status = true, StatusDisabled
				if _, err := m.Update(ctx, current); err != nil {
					return nil, err
				}
				a.Metadata["access_token"] = "refreshed"
				a.Metadata["project_id"] = "discovered"
				return a, nil
			}
			m.RegisterExecutor(exec)
			if prepare {
				_, err = m.prepareRequestAuth(ctx, exec, base)
			} else {
				_, err = m.RefreshAuth(ctx, base.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			current, _ := m.GetByID(base.ID)
			for _, got := range []*Auth{current, store.lastAuth()} {
				if !got.Disabled || got.Status != StatusDisabled || got.ProxyURL != "http://new.proxy:8080" || got.Metadata["note"] != "operator" || got.Attributes["priority"] != "9" {
					t.Fatal("interleaved operator change was overwritten")
				}
				if _, exists := got.Metadata["remove"]; exists {
					t.Fatal("deleted metadata was restored")
				}
				if got.Metadata["access_token"] != "refreshed" || got.Metadata["project_id"] != "discovered" {
					t.Fatal("executor changes were discarded")
				}
			}
		})
	}
}

func TestAuthRefreshRejectsRemovedAndReplacedRegistration(t *testing.T) {
	for _, replace := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			ctx := context.Background()
			store := &requestPrepareStore{}
			m := NewManager(store, nil, nil)
			base, _ := m.Register(ctx, &Auth{ID: "account", Provider: "codex", Metadata: map[string]any{"access_token": "old"}})
			exec := interleavedAuthExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
			exec.change = func(a *Auth) (*Auth, error) {
				m.Remove(ctx, base.ID)
				if replace {
					_, _ = m.Register(ctx, &Auth{ID: base.ID, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "replacement"}})
				}
				if fail {
					return nil, errors.New("refresh failed: 401 unauthorized")
				}
				a.Metadata["access_token"] = "stale-refresh"
				return a, nil
			}
			m.RegisterExecutor(exec)
			_, err := m.RefreshAuth(ctx, base.ID)
			if err == nil {
				t.Fatalf("replace=%v fail=%v: stale refresh succeeded", replace, fail)
			}
			count := store.saveCount.Load()
			_ = m.persist(ctx, base)
			if store.saveCount.Load() != count {
				t.Fatal("deleted registration was persisted")
			}
			got, ok := m.GetByID(base.ID)
			if ok != replace {
				t.Fatal("removed auth was restored")
			}
			if replace && (got.Metadata["access_token"] != "replacement" || got.LastError != nil) {
				t.Fatal("replacement auth was changed by old refresh")
			}
		}
	}
}

func TestAuthPersistenceRejectsDelayedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := &requestPrepareStore{}
	m := NewManager(store, nil, nil)
	base, _ := m.Register(ctx, &Auth{ID: "account", Metadata: map[string]any{"note": "old"}})
	latest := base.Clone()
	latest.Metadata["note"] = "new"
	_, _ = m.Update(ctx, latest)
	_ = m.persist(ctx, base)
	if store.lastAuth().Metadata["note"] != "new" {
		t.Fatal("older snapshot overwrote new metadata")
	}
	latest.Metadata["note"] = "external"
	_, _ = m.Update(WithSkipPersist(ctx), latest)
	count := store.saveCount.Load()
	_ = m.persist(ctx, base)
	if store.saveCount.Load() != count {
		t.Fatal("skip-persist update failed to fence older writes")
	}
}

func TestAuthRefreshRecoversUnauthorizedButPreservesConcurrentQuota(t *testing.T) {
	for _, concurrentQuota := range []bool{false, true} {
		ctx := context.Background()
		m := NewManager(nil, nil, nil)
		base, _ := m.Register(ctx, &Auth{ID: "recovery", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
		m.MarkResult(ctx, Result{AuthID: base.ID, Provider: "codex", Model: "model", Error: &Error{HTTPStatus: 401, Code: "unauthorized"}})
		exec := interleavedAuthExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
		exec.change = func(a *Auth) (*Auth, error) {
			if concurrentQuota {
				delay := time.Hour
				m.MarkResult(ctx, Result{AuthID: base.ID, Provider: "codex", Model: "model", CredentialScope: true, RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "quota"}})
			}
			a.Metadata["access_token"] = "new"
			return a, nil
		}
		m.RegisterExecutor(exec)
		got, err := m.RefreshAuth(ctx, base.ID)
		if err != nil {
			t.Fatal(err)
		}
		blocked, _, _ := isAuthBlockedForModel(got, "model", time.Now())
		if blocked != concurrentQuota {
			t.Fatalf("concurrent quota=%v, blocked=%v", concurrentQuota, blocked)
		}
		if !concurrentQuota && (got.Status != StatusActive || got.LastError != nil || got.Unavailable) {
			t.Fatal("successful refresh did not recover old unauthorized state")
		}
	}
}

func TestAuthRefreshPreservesNewerTokenUpdate(t *testing.T) {
	ctx := context.Background()
	store := &requestPrepareStore{}
	m := NewManager(store, nil, nil)
	base, _ := m.Register(ctx, &Auth{ID: "token-update", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
	exec := interleavedAuthExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
	exec.change = func(a *Auth) (*Auth, error) {
		current, _ := m.GetByID(base.ID)
		current.Metadata["access_token"] = "newer-login"
		_, err := m.Update(ctx, current)
		a.Metadata["access_token"] = "older-refresh"
		return a, err
	}
	m.RegisterExecutor(exec)
	got, err := m.RefreshAuth(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["access_token"] != "newer-login" || store.lastAuth().Metadata["access_token"] != "newer-login" {
		t.Fatal("newer credentials were overwritten")
	}
}

func TestQuotaScopeAndSubsecondCooldown(t *testing.T) {
	now := time.Now()
	model := "model-a"
	a := &Auth{Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}, ModelStates: map[string]*ModelState{model: {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}}
	if blocked, _, _ := isAuthBlockedForModel(a, "", now); blocked {
		t.Fatal("single-model quota blocks entire credential")
	}
	if blocked, _, _ := isAuthBlockedForModel(a, model, now); !blocked {
		t.Fatal("limited model was allowed")
	}
	a.Quota.Reason = "credential_quota"
	if blocked, _, _ := isAuthBlockedForModel(a, "model-b", now); !blocked {
		t.Fatal("credential quota does not block sibling model")
	}
	for _, credential := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			m := NewManager(nil, nil, nil)
			registered, _ := m.Register(context.Background(), &Auth{ID: "floor", Provider: "codex", Metadata: map[string]any{"disable_cooling": disabled}})
			delay := time.Millisecond
			result := Result{AuthID: registered.ID, Provider: "codex", Model: model, CredentialScope: credential, RetryAfter: &delay, Error: &Error{HTTPStatus: 429}}
			before := time.Now()
			m.MarkResult(context.Background(), result)
			got, _ := m.GetByID(registered.ID)
			reset := got.ModelStates[model].NextRetryAfter
			if disabled {
				if !reset.IsZero() {
					t.Fatal("cooling disable ignored")
				}
			} else if reset.Before(before.Add(10 * time.Second)) {
				t.Fatal("subsecond quota cooldown was not floored")
			}
			if !disabled {
				m.MarkResult(context.Background(), result)
				again, _ := m.GetByID(registered.ID)
				if again.ModelStates[model].Quota.BackoffLevel != got.ModelStates[model].Quota.BackoffLevel {
					t.Fatal("concurrent failure escalated backoff")
				}
			}
		}
	}
}

type scopedStreamError struct{ delay time.Duration }

func (e scopedStreamError) Error() string              { return "usage limit reached" }
func (e scopedStreamError) StatusCode() int            { return http.StatusTooManyRequests }
func (e scopedStreamError) IsCredentialScoped() bool   { return true }
func (e scopedStreamError) RetryAfter() *time.Duration { return &e.delay }

func TestStreamQuotaRetainsCredentialScopeAndReset(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	a, _ := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex"})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "a"}, {ID: "b"}})
	t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Err: scopedStreamError{delay: time.Hour}}
	close(remaining)
	before := time.Now()
	result := m.wrapStreamResult(ctx, a, "codex", "a", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{})
	for range result.Chunks {
	}
	got, _ := m.GetByID(a.ID)
	if got.Quota.Reason != "credential_quota" || got.NextRetryAfter.Before(before.Add(time.Hour)) {
		t.Fatal("stream quota scope/reset was lost")
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "b", time.Now()); !blocked {
		t.Fatal("sibling model remained available")
	}
}

func TestQuotaSelectionRetainsHealthySiblingModel(t *testing.T) {
	for _, selector := range []Selector{&RoundRobinSelector{}, &FillFirstSelector{}, &WeightedRoundRobinSelector{}, NewSessionAffinitySelector(&RoundRobinSelector{})} {
		ctx := context.Background()
		m := NewManager(nil, selector, nil)
		m.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
		a, _ := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex"})
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "limited"}, {ID: "healthy"}})
		delay := time.Hour
		m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "codex", Model: "limited", RetryAfter: &delay, Error: &Error{HTTPStatus: 429}})
		opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"quota-test"}}}
		got, _, err := m.pickNextLegacy(ctx, "codex", "healthy", opts, nil)
		if err != nil || got == nil {
			t.Errorf("%T rejected healthy sibling: %v", selector, err)
		}
		if _, _, err = m.pickNextLegacy(ctx, "codex", "limited", opts, nil); err == nil {
			t.Errorf("%T accepted limited model", selector)
		}
		reg.UnregisterClient(a.ID)
		if stopper, ok := selector.(StoppableSelector); ok {
			stopper.Stop()
		}
	}
}
