package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (h *Handler) GetOpenCodeGoKeys(c *gin.Context) {
	c.JSON(200, gin.H{"opencode-go-api-key": h.openCodeGoKeysWithAuthIndex()})
}

func (h *Handler) PutOpenCodeGoKeys(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries []config.OpenCodeGoKey
	if errJSON := json.Unmarshal(data, &entries); errJSON != nil {
		var object struct {
			Items []config.OpenCodeGoKey `json:"items"`
		}
		if errObject := json.Unmarshal(data, &object); errObject != nil || len(object.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = object.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OpenCodeGoKey = entries
	h.cfg.SanitizeOpenCodeGoKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchOpenCodeGoKey(c *gin.Context) {
	type keyPatch struct {
		Name           *string                   `json:"name"`
		APIKey         *string                   `json:"api-key"`
		Priority       *int                      `json:"priority"`
		Prefix         *string                   `json:"prefix"`
		BaseURL        *string                   `json:"base-url"`
		ProxyURL       *string                   `json:"proxy-url"`
		Models         *[]config.OpenCodeGoModel `json:"models"`
		Headers        *map[string]string        `json:"headers"`
		ExcludedModels *[]string                 `json:"excluded-models"`
		DisableCooling *bool                     `json:"disable-cooling"`
	}
	var body struct {
		Index *int      `json:"index"`
		Match *string   `json:"match"`
		Value *keyPatch `json:"value"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	index := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenCodeGoKey) {
		index = *body.Index
	}
	if index == -1 && body.Match != nil {
		for i := range h.cfg.OpenCodeGoKey {
			if h.cfg.OpenCodeGoKey[i].APIKey == strings.TrimSpace(*body.Match) {
				index = i
				break
			}
		}
	}
	if index == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	entry := h.cfg.OpenCodeGoKey[index]
	if body.Value.Name != nil {
		entry.Name = *body.Value.Name
	}
	if body.Value.APIKey != nil {
		entry.APIKey = *body.Value.APIKey
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if body.Value.Prefix != nil {
		entry.Prefix = *body.Value.Prefix
	}
	if body.Value.BaseURL != nil {
		entry.BaseURL = *body.Value.BaseURL
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = *body.Value.ProxyURL
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.OpenCodeGoModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.ExcludedModels != nil {
		entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
	}
	if body.Value.DisableCooling != nil {
		entry.DisableCooling = *body.Value.DisableCooling
	}
	h.cfg.OpenCodeGoKey[index] = entry
	h.cfg.SanitizeOpenCodeGoKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteOpenCodeGoKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if key := strings.TrimSpace(c.Query("api-key")); key != "" {
		out := make([]config.OpenCodeGoKey, 0, len(h.cfg.OpenCodeGoKey))
		for _, entry := range h.cfg.OpenCodeGoKey {
			if strings.TrimSpace(entry.APIKey) != key {
				out = append(out, entry)
			}
		}
		h.cfg.OpenCodeGoKey = out
		h.persistLocked(c)
		return
	}
	if rawIndex := c.Query("index"); rawIndex != "" {
		var index int
		if _, errScan := fmt.Sscanf(rawIndex, "%d", &index); errScan == nil && index >= 0 && index < len(h.cfg.OpenCodeGoKey) {
			h.cfg.OpenCodeGoKey = append(h.cfg.OpenCodeGoKey[:index], h.cfg.OpenCodeGoKey[index+1:]...)
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}
