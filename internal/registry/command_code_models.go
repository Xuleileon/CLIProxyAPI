package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

//go:embed models/command_code_models.json
var commandCodeBuiltins []byte

var commandCodeCatalog = struct {
	sync.RWMutex
	models []*ModelInfo
}{}

// GetCommandCodeModels returns a copy of the catalog, with an official snapshot as fallback.
func GetCommandCodeModels() []*ModelInfo {
	commandCodeCatalog.RLock()
	defer commandCodeCatalog.RUnlock()
	if len(commandCodeCatalog.models) > 0 {
		return cloneModelInfos(commandCodeCatalog.models)
	}
	models, _ := parseCommandCodeCatalog(commandCodeBuiltins)
	return models
}

func commandCodeModel(id, name string, contextLength int) *ModelInfo {
	return &ModelInfo{ID: id, Object: "model", OwnedBy: "command-code", Type: "openai", DisplayName: name, ContextLength: contextLength,
		SupportedEndpoints: []string{"/chat/completions", "/messages", "/responses"},
		Thinking:           &ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}}}
}

func runCommandCodeModelsUpdater(ctx context.Context) {
	refresh := func() {
		if err := refreshCommandCodeModels(ctx, http.DefaultClient, "https://api.commandcode.ai/provider/v1/models"); err != nil {
			log.Warnf("command-code catalog refresh failed: %v", err)
		}
	}
	refresh()
	ticker := time.NewTicker(modelsRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func refreshCommandCodeModels(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("command-code catalog close: %v", errClose)
		}
	}()
	if resp.StatusCode != 200 {
		return fmt.Errorf("catalog returned HTTP %d", resp.StatusCode)
	}
	var raw json.RawMessage
	if err = json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return err
	}
	models, err := parseCommandCodeCatalog(raw)
	if err != nil {
		return err
	}
	commandCodeCatalog.Lock()
	changed := modelSectionChanged(commandCodeCatalog.models, models)
	commandCodeCatalog.models = models
	commandCodeCatalog.Unlock()
	if changed {
		notifyModelRefresh([]string{"command-code"})
	}
	return nil
}

func parseCommandCodeCatalog(raw []byte) ([]*ModelInfo, error) {
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	models := make([]*ModelInfo, 0, len(body.Data))
	seen := map[string]bool{}
	for _, m := range body.Data {
		id := strings.TrimSpace(m.ID)
		// The catalog does not carry account entitlements. In particular, filtering
		// by vendor would incorrectly remove Go's GPT Luna and Muse exceptions.
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, commandCodeModel(id, m.Name, m.ContextLength))
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("empty command-code catalog")
	}
	return models, nil
}
