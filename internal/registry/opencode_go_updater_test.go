package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenCodeGoRefreshReplacesCatalogAndNotifies(t *testing.T) {
	openCodeGoCatalog.Lock()
	original := openCodeGoCatalog.models
	openCodeGoCatalog.models = nil
	openCodeGoCatalog.Unlock()
	refreshCallbackMu.Lock()
	originalCallback, originalPending := refreshCallback, pendingRefreshChanges
	pendingRefreshChanges = nil
	refreshCallbackMu.Unlock()
	t.Cleanup(func() {
		openCodeGoCatalog.Lock()
		openCodeGoCatalog.models = original
		openCodeGoCatalog.Unlock()
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = originalCallback, originalPending
		refreshCallbackMu.Unlock()
	})
	status := http.StatusOK
	body := `{"data":[{"id":"qwen3.8-flash"},{"id":"deepseek-flash"},{"id":"muse-spark-1.2-contributor"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	refresh := func() error { return refreshOpenCodeGoModels(context.Background(), server.Client(), server.URL) }
	calls := 0
	SetModelRefreshCallback(func(providers []string) {
		calls++
		if len(providers) != 1 || providers[0] != "opencode-go" {
			t.Errorf("providers = %v", providers)
		}
		// The callback must observe the new catalog without holding its lock.
		if len(GetOpenCodeGoModels()) == 0 {
			t.Error("empty catalog in callback")
		}
	})
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	models := GetOpenCodeGoModels()
	if len(models) != 3 || calls != 1 {
		t.Fatalf("models=%d notifications=%d", len(models), calls)
	}
	for _, model := range models {
		want := map[string]string{"deepseek-flash": "/chat/completions", "qwen3.8-flash": "/messages", "muse-spark-1.2-contributor": "/responses"}[model.ID]
		if model.OwnedBy != "opencode-go" || len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != want {
			t.Fatalf("wrong route: %+v", model)
		}
	}
	models[0].ID = "mutated"
	if GetOpenCodeGoModels()[0].ID == "mutated" {
		t.Fatal("caller mutated shared catalog")
	}
	body = `{"data":[{"id":"muse-spark-1.2-contributor","created":123},{"id":"deepseek-flash"},{"id":"qwen3.8-flash"}]}`
	if err := refresh(); err != nil || calls != 1 {
		t.Fatalf("reordered catalog retriggered refresh: %v, calls=%d", err, calls)
	}
	for _, invalid := range []string{`{`, `{"data":[]}`, `{"data":[null]}`, `{"data":[{"id":""}]}`, `{"data":[{"id":"a"},{"id":" a "}]}`} {
		body = invalid
		if err := refresh(); err == nil {
			t.Fatalf("accepted invalid catalog %s", invalid)
		}
		if len(GetOpenCodeGoModels()) != 3 || calls != 1 {
			t.Fatal("invalid response changed catalog")
		}
	}
	status = http.StatusServiceUnavailable
	if err := refresh(); err == nil || len(GetOpenCodeGoModels()) != 3 {
		t.Fatal("HTTP failure discarded catalog")
	}
	status = http.StatusOK
	body = `{"data":[{"id":"deepseek-flash"},{"id":"new-model"}]}`
	if err := refresh(); err != nil || calls != 2 || len(GetOpenCodeGoModels()) != 2 {
		t.Fatalf("subsequent update failed: %v, calls=%d", err, calls)
	}
}
