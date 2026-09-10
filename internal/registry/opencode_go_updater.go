package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const openCodeGoModelsURL = "https://opencode.ai/zen/go/v1/models"

// runOpenCodeGoModelsUpdater runs independently of the generic catalog so a
// failure fetching either source cannot prevent the other provider's refresh.
func runOpenCodeGoModelsUpdater(ctx context.Context) {
	refresh := func() {
		if err := refreshOpenCodeGoModels(ctx, http.DefaultClient, openCodeGoModelsURL); err != nil {
			log.Warnf("opencode-go model refresh failed; keeping current catalog: %v", err)
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

func refreshOpenCodeGoModels(ctx context.Context, client *http.Client, url string) error {
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
			log.Debugf("opencode-go models: close response: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog returned HTTP %d", resp.StatusCode)
	}
	var catalog struct {
		Data []*ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return fmt.Errorf("decode catalog: %w", err)
	}
	if len(catalog.Data) == 0 {
		return fmt.Errorf("catalog is empty")
	}
	if err := validateModelSection("opencode-go", catalog.Data); err != nil {
		return err
	}
	ids := make([]string, 0, len(catalog.Data))
	for _, model := range catalog.Data {
		ids = append(ids, strings.TrimSpace(model.ID))
	}
	// The endpoint includes changing creation timestamps but no protocol metadata.
	// Normalize IDs and use the same protocol mapping as the request executor.
	sort.Strings(ids)
	models := buildOpenCodeGoModels(ids)
	openCodeGoCatalog.Lock()
	changed := modelSectionChanged(openCodeGoCatalog.models, models)
	openCodeGoCatalog.models = models
	openCodeGoCatalog.Unlock()
	if changed {
		log.Infof("opencode-go model catalog refreshed: %d models", len(models))
		notifyModelRefresh([]string{"opencode-go"})
	}
	return nil
}
