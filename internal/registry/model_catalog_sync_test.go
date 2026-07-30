package registry

import "testing"

// TestSelectedSyncBaseline verifies that the B003 selected-sync entries added
// from upstream v7.2.105 are present in the embedded catalog with valid fields.
// These are the 9 (provider, id) records synced in Batch 3.
func TestSelectedSyncBaseline(t *testing.T) {
	type entry struct {
		provider string
		id       string
	}
	selected := []entry{
		{"claude", "claude-opus-5"},
		{"gemini", "gemini-3.5-flash-lite"},
		{"gemini", "gemini-3.6-flash"},
		{"vertex", "gemini-3.1-pro-preview"},
		{"vertex", "gemini-3.5-flash-lite"},
		{"vertex", "gemini-3.6-flash"},
		{"aistudio", "gemini-3.5-flash-lite"},
		{"aistudio", "gemini-3.6-flash"},
		{"antigravity", "gemini-3.6-flash-high"},
	}

	getters := map[string]func() []*ModelInfo{
		"claude":      GetClaudeModels,
		"gemini":      GetGeminiModels,
		"vertex":      GetGeminiVertexModels,
		"aistudio":    GetAIStudioModels,
		"antigravity": GetAntigravityModels,
	}

	for _, e := range selected {
		getter, ok := getters[e.provider]
		if !ok {
			t.Fatalf("no getter for provider %q", e.provider)
		}
		models := getter()
		var found *ModelInfo
		for _, m := range models {
			if m.ID == e.id {
				found = m
				break
			}
		}
		if found == nil {
			t.Errorf("(%s, %s): not found in embedded catalog", e.provider, e.id)
			continue
		}
		if found.DisplayName == "" {
			t.Errorf("(%s, %s): empty display_name", e.provider, e.id)
		}
		// All selected entries must have either context_length or inputTokenLimit set.
		if found.ContextLength == 0 && found.InputTokenLimit == 0 {
			t.Errorf("(%s, %s): both context_length and inputTokenLimit are zero", e.provider, e.id)
		}
	}
}

// TestSelectedSyncBaselineNoDuplicateProviderIDs verifies no (provider, id)
// duplicates exist across all providers in the embedded catalog.
func TestSelectedSyncBaselineNoDuplicateProviderIDs(t *testing.T) {
	providers := map[string]func() []*ModelInfo{
		"claude":      GetClaudeModels,
		"gemini":      GetGeminiModels,
		"vertex":      GetGeminiVertexModels,
		"gemini-cli":  GetGeminiCLIModels,
		"aistudio":    GetAIStudioModels,
		"codex-free":  GetCodexFreeModels,
		"codex-team":  GetCodexTeamModels,
		"codex-plus":  GetCodexPlusModels,
		"codex-pro":   GetCodexProModels,
		"kimi":        GetKimiModels,
		"antigravity": GetAntigravityModels,
		"xai":         GetXAIModels,
		"qoder":       GetQoderModels,
	}
	for prov, getter := range providers {
		seen := make(map[string]bool)
		for _, m := range getter() {
			if m.ID == "" {
				continue
			}
			if seen[m.ID] {
				t.Errorf("duplicate id %q in provider %q", m.ID, prov)
			}
			seen[m.ID] = true
		}
	}
}

// TestSelectedSyncBaselineQoderProtected verifies fork-specific qoder entries
// survive the B003 sync and are non-empty.
func TestSelectedSyncBaselineQoderProtected(t *testing.T) {
	models := GetQoderModels()
	if len(models) == 0 {
		t.Fatal("qoder models empty after B003 sync; fork entries must be preserved")
	}
	for _, m := range models {
		if m.ID == "" {
			t.Error("qoder model with empty id found")
		}
	}
}
