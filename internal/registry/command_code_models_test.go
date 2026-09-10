package registry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommandCodeCatalogUsesAPIAndKeepsLastGood(t *testing.T) {
	commandCodeCatalog.Lock()
	previous := commandCodeCatalog.models
	commandCodeCatalog.models = nil
	commandCodeCatalog.Unlock()
	defer func() { commandCodeCatalog.Lock(); commandCodeCatalog.models = previous; commandCodeCatalog.Unlock() }()
	bad := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bad {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek/deepseek-v4-flash","name":"Flash","context_length":1048576},{"id":"claude-opus-5"},{"id":"google/gemini-3.8-flash"}]}`)
	}))
	defer s.Close()
	if err := refreshCommandCodeModels(context.Background(), s.Client(), s.URL); err != nil {
		t.Fatal(err)
	}
	models := GetCommandCodeModels()
	if len(models) != 3 || models[0].ContextLength != 1048576 {
		t.Fatal("incorrect catalog")
	}
	models[0].ID = "mutated"
	if GetCommandCodeModels()[0].ID == "mutated" {
		t.Fatal("shared catalog escaped")
	}
	bad = true
	if err := refreshCommandCodeModels(context.Background(), s.Client(), s.URL); err == nil {
		t.Fatal("accepted empty catalog")
	}
	if len(GetCommandCodeModels()) != 3 {
		t.Fatal("lost last good catalog")
	}
}
