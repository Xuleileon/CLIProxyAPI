// Package models builds model catalogs for Anthropic clients.
package models

import (
	"sort"
	"strings"
)

const claudeDDModelPrefix = "claude-fable-5-dd-"

// BuildResponse builds an Anthropic model response from available models.
// Each registry model is listed once with its original ID so the catalog size
// matches the backend. Cloaked IDs remain accepted by FindModel and request routing.
func BuildResponse(availableModels []map[string]any, _ bool) map[string]any {
	models := make([]map[string]any, 0, len(availableModels))
	for _, model := range availableModels {
		models = append(models, cloneModel(model))
	}

	sort.SliceStable(models, func(i, j int) bool {
		displayNameI, _ := models[i]["display_name"].(string)
		displayNameJ, _ := models[j]["display_name"].(string)
		if displayNameI != displayNameJ {
			return displayNameI < displayNameJ
		}
		idI, _ := models[i]["id"].(string)
		idJ, _ := models[j]["id"].(string)
		return idI < idJ
	})

	firstID := ""
	lastID := ""
	if len(models) > 0 {
		firstID, _ = models[0]["id"].(string)
		lastID, _ = models[len(models)-1]["id"].(string)
	}

	return map[string]any{
		"data":     models,
		"has_more": false,
		"first_id": firstID,
		"last_id":  lastID,
	}
}

// FindModel returns the listed Anthropic model matching id.
// It accepts both the listed id (including cloaked ids) and the original registry id.
func FindModel(availableModels []map[string]any, id string, disableCloaking bool) (map[string]any, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}

	listed, _ := BuildResponse(availableModels, disableCloaking)["data"].([]map[string]any)
	want := ResolveClaudeModelIDPrefix(id)
	var fuzzy map[string]any
	for _, model := range listed {
		listedID, _ := model["id"].(string)
		if listedID == id {
			return model, true
		}
		if fuzzy == nil && (listedID == want || ResolveClaudeModelIDPrefix(listedID) == want) {
			fuzzy = model
		}
	}
	if fuzzy != nil {
		return fuzzy, true
	}
	return nil, false
}

// EnsureClaudeModelIDPrefix rewrites model IDs for Anthropic model listings.
// IDs that already start with "claude-" are returned unchanged; all other IDs
// become "claude-fable-5-dd-" plus the original ID with its characters reversed.
func EnsureClaudeModelIDPrefix(id string) string {
	if id == "" || strings.HasPrefix(id, "claude-") {
		return id
	}
	return claudeDDModelPrefix + reverseModelID(id)
}

// ResolveClaudeModelIDPrefix reverses EnsureClaudeModelIDPrefix for request routing.
// Optional thinking suffixes in model(value) form are preserved.
func ResolveClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}
	base, suffix, hasSuffix := splitModelThinkingSuffix(id)
	if !strings.HasPrefix(base, claudeDDModelPrefix) {
		return id
	}
	encoded := base[len(claudeDDModelPrefix):]
	if encoded == "" {
		return id
	}
	resolved := reverseModelID(encoded)
	if hasSuffix {
		return resolved + "(" + suffix + ")"
	}
	return resolved
}

func cloneModel(model map[string]any) map[string]any {
	cloned := make(map[string]any, len(model))
	for key, value := range model {
		cloned[key] = value
	}
	return cloned
}

func splitModelThinkingSuffix(model string) (base, suffix string, hasSuffix bool) {
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	return model[:lastOpen], model[lastOpen+1 : len(model)-1], true
}

func reverseModelID(id string) string {
	runes := []rune(id)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
