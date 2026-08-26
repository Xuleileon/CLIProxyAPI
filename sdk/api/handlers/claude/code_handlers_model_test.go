package claude

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestClaudeRetrieveModel(t *testing.T) {
	const clientID = "claude-retrieve-model-test"
	const modelID = "claude-retrieve-model-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test", DisplayName: "Retrieve Claude",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})

	t.Run("found", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "model", Value: modelID}}
		handler.ClaudeRetrieveModel(ctx)
		if recorder.Code != 200 {
			t.Fatalf("status = %d, want 200 body=%s", recorder.Code, recorder.Body.String())
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "id").String(); got != modelID {
			t.Fatalf("id = %q, want %q", got, modelID)
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "display_name").String(); got != "Retrieve Claude" {
			t.Fatalf("display_name = %q, want Retrieve Claude", got)
		}
	})

	t.Run("missing", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "model", Value: "missing-model"}}
		handler.ClaudeRetrieveModel(ctx)
		if recorder.Code != 404 {
			t.Fatalf("status = %d, want 404 body=%s", recorder.Code, recorder.Body.String())
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "type").String(); got != "error" {
			t.Fatalf("type = %q, want error", got)
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got != "not_found_error" {
			t.Fatalf("error.type = %q, want not_found_error", got)
		}
	})
}

func TestClaudeModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "claude-display-name-catalog-test"
	const modelID = "claude-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test", DisplayName: "Configured Claude Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			if model.DisplayName != "Configured Claude Name" {
				t.Fatalf("display_name = %q, want Configured Claude Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}

func TestClaudeModelsResponseDisablesModelListCloaking(t *testing.T) {
	const clientID = "claude-disable-model-list-cloaking-test"
	const modelID = "gpt-disable-model-list-cloaking-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	baseHandler := &handlers.BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{
		ClaudeCode: sdkconfig.ClaudeCodeConfig{DisableCloakingModelList: true},
	}}
	NewClaudeCodeAPIHandler(baseHandler).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			return
		}
	}
	t.Fatalf("uncloaked model %q not found in response", modelID)
}

func TestRewriteClaudeDDModelInBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
	}{
		{
			name:      "encoded model is decoded",
			body:      `{"model":"claude-fable-5-dd-o4-tpg","messages":[]}`,
			wantModel: "gpt-4o",
		},
		{
			name:      "plain claude model unchanged",
			body:      `{"model":"claude-sonnet-4-6","messages":[]}`,
			wantModel: "claude-sonnet-4-6",
		},
		{
			name:      "encoded model with thinking suffix",
			body:      `{"model":"claude-fable-5-dd-o4-tpg(high)","stream":true}`,
			wantModel: "gpt-4o(high)",
		},
		{
			name:      "missing model field unchanged",
			body:      `{"messages":[]}`,
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteClaudeDDModelInBody([]byte(tt.body))
			if model := gjson.GetBytes(got, "model").String(); model != tt.wantModel {
				t.Fatalf("model = %q, want %q; body=%s", model, tt.wantModel, string(got))
			}
		})
	}
}
