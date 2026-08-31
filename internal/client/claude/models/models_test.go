package models

import "testing"

func TestBuildResponse(t *testing.T) {
	availableModels := []map[string]any{
		{"id": "claude-z", "display_name": "Zebra", "max_tokens": 64000},
		{"id": "gpt-4o", "display_name": "Alpha"},
		{"id": "claude-c", "display_name": "Alpha"},
		{"id": "claude-b", "display_name": "Beta"},
	}

	response := BuildResponse(availableModels, false)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}

	wantIDs := []string{
		"claude-c",
		"gpt-4o",
		"claude-b",
		"claude-z",
	}
	if len(models) != len(wantIDs) {
		t.Fatalf("len(data) = %d, want %d", len(models), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got, _ := models[i]["id"].(string); got != want {
			t.Fatalf("data[%d].id = %q, want %q", i, got, want)
		}
	}
	if got := models[3]["max_tokens"]; got != 64000 {
		t.Fatalf("max_tokens = %v, want 64000", got)
	}
	if got := response["has_more"]; got != false {
		t.Fatalf("has_more = %v, want false", got)
	}
	if got := response["first_id"]; got != wantIDs[0] {
		t.Fatalf("first_id = %v, want %q", got, wantIDs[0])
	}
	if got := response["last_id"]; got != wantIDs[len(wantIDs)-1] {
		t.Fatalf("last_id = %v, want %q", got, wantIDs[len(wantIDs)-1])
	}

	if got := availableModels[1]["id"]; got != "gpt-4o" {
		t.Fatalf("BuildResponse mutated input id to %v", got)
	}
	if got := availableModels[0]["id"]; got != "claude-z" {
		t.Fatalf("BuildResponse reordered input: first id = %v", got)
	}
}

func TestBuildResponseWithCloakingDisabled(t *testing.T) {
	availableModels := []map[string]any{
		{"id": "gpt-4o", "display_name": "GPT-4o"},
	}

	response := BuildResponse(availableModels, true)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}
	if len(models) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(models))
	}
	if got := models[0]["id"]; got != "gpt-4o" {
		t.Fatalf("data[0].id = %v, want gpt-4o", got)
	}
	if got := response["first_id"]; got != "gpt-4o" {
		t.Fatalf("first_id = %v, want gpt-4o", got)
	}
	if got := response["last_id"]; got != "gpt-4o" {
		t.Fatalf("last_id = %v, want gpt-4o", got)
	}
}

func TestBuildResponseEmpty(t *testing.T) {
	response := BuildResponse(nil, false)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}
	if len(models) != 0 {
		t.Fatalf("len(data) = %d, want 0", len(models))
	}
	if response["first_id"] != "" || response["last_id"] != "" {
		t.Fatalf("empty response IDs = (%v, %v), want empty", response["first_id"], response["last_id"])
	}
}

func TestEnsureClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"already has claude prefix", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"contains claude mid-string is reversed", "my-claude-custom", "claude-fable-5-dd-motsuc-edualc-ym"},
		{"uppercase Claude prefix is reversed", "Claude-Opus-4", "claude-fable-5-dd-4-supO-edualC"},
		{"gpt model is reversed", "gpt-4o", "claude-fable-5-dd-o4-tpg"},
		{"codex gpt model is reversed", "gpt-5.6-sol", "claude-fable-5-dd-los-6.5-tpg"},
		{"codex gpt effort variant is reversed", "gpt-5.6-sol-low-fast", "claude-fable-5-dd-tsaf-wol-los-6.5-tpg"},
		{"gemini model is reversed", "gemini-2.5-pro", "claude-fable-5-dd-orp-5.2-inimeg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnsureClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("EnsureClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestFindModel(t *testing.T) {
	availableModels := []map[string]any{
		{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6", "max_tokens": 64000},
		{"id": "gpt-4o", "display_name": "GPT-4o"},
	}

	t.Run("finds listed claude id", func(t *testing.T) {
		model, ok := FindModel(availableModels, "claude-sonnet-4-6", false)
		if !ok {
			t.Fatal("expected model")
		}
		if got, _ := model["id"].(string); got != "claude-sonnet-4-6" {
			t.Fatalf("id = %q, want claude-sonnet-4-6", got)
		}
	})

	t.Run("finds cloaked listing id", func(t *testing.T) {
		model, ok := FindModel(availableModels, "claude-fable-5-dd-o4-tpg", false)
		if !ok {
			t.Fatal("expected cloaked model")
		}
		if got, _ := model["id"].(string); got != "gpt-4o" {
			t.Fatalf("id = %q, want gpt-4o", got)
		}
	})

	t.Run("finds original id when cloaking disabled", func(t *testing.T) {
		model, ok := FindModel(availableModels, "gpt-4o", true)
		if !ok {
			t.Fatal("expected uncloaked model")
		}
		if got, _ := model["id"].(string); got != "gpt-4o" {
			t.Fatalf("id = %q, want gpt-4o", got)
		}
	})

	t.Run("finds original gpt id when cloaking enabled", func(t *testing.T) {
		model, ok := FindModel(availableModels, "gpt-4o", false)
		if !ok {
			t.Fatal("expected original gpt model")
		}
		if got, _ := model["id"].(string); got != "gpt-4o" {
			t.Fatalf("id = %q, want gpt-4o", got)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		if _, ok := FindModel(availableModels, "missing-model", false); ok {
			t.Fatal("expected miss")
		}
	})
}

func TestResolveClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"plain claude id unchanged", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"non encoded id unchanged", "gpt-4o", "gpt-4o"},
		{"encoded gpt model", "claude-fable-5-dd-o4-tpg", "gpt-4o"},
		{"encoded gemini model", "claude-fable-5-dd-orp-5.2-inimeg", "gemini-2.5-pro"},
		{"empty encoded body unchanged", "claude-fable-5-dd-", "claude-fable-5-dd-"},
		{"preserves thinking suffix", "claude-fable-5-dd-o4-tpg(high)", "gpt-4o(high)"},
		{"round trip", EnsureClaudeModelIDPrefix("custom-model-x"), "custom-model-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("ResolveClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
