package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	geminiAuth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gemini"
	gitlabauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gitlab"
	iflowauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

const (
	anthropicCallbackPort = 54545
	codexCallbackPort     = 1455
	geminiCLIEndpoint     = "https://cloudcode-pa.googleapis.com"
	geminiCLIVersion      = "v1internal"
	gitLabLoginModeOAuth  = "oauth"
	gitLabLoginModePAT    = "pat"
)

type callbackForwarder struct {
	provider string
	server   *http.Server
	done     chan struct{}
}

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

var (
	callbackForwardersMu  sync.Mutex
	callbackForwarders    = make(map[int]*callbackForwarder)
	authFileEntryMu       sync.Mutex
	errAuthFileMustBeJSON = errors.New("auth file must be .json")
	errAuthFileNotFound   = errors.New("auth file not found")
	errPluginVirtualAuth  = errors.New("plugin virtual auth cannot be modified directly; edit or delete the source auth file")
	newCodexOAuthService  = func(cfg *config.Config) codexOAuthService { return codex.NewCodexAuth(cfg) }
)

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func isWebUIRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("is_webui"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startCallbackForwarder(port int, provider, targetBase string) (*callbackForwarder, error) {
	callbackForwardersMu.Lock()
	prev := callbackForwarders[port]
	if prev != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	if prev != nil {
		stopForwarderInstance(port, prev)
	}

	addr := fmt.Sprintf("%s:%d", callbackForwarderListenHost(), port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if strings.Contains(target, "?") {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &callbackForwarder{
		provider: provider,
		server:   srv,
		done:     done,
	}

	callbackForwardersMu.Lock()
	callbackForwarders[port] = forwarder
	callbackForwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

func callbackForwarderListenHost() string {
	return callbackForwarderListenHostForRuntime(runningInContainer())
}

func callbackForwarderListenHostForRuntime(inContainer bool) string {
	if inContainer || isTruthyEnv(os.Getenv("CLIPROXY_CALLBACK_FORWARDER_BIND_ALL")) {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DEPLOY")), "docker")
}

func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func stopCallbackForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil {
		return
	}
	callbackForwardersMu.Lock()
	if current := callbackForwarders[port]; current == forwarder {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	stopForwarderInstance(port, forwarder)
}

func stopForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", port)
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path), nil
}

func pluginAuthProviderFromPath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	const prefix = "/v0/management/"
	const suffix = "-auth-url"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	provider := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return "", false
	}
	for _, r := range provider {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", false
		}
	}
	return provider, true
}

func (h *Handler) ServePluginAuthURL(c *gin.Context) bool {
	if h == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if host == nil {
		return false
	}
	provider, ok := pluginAuthProviderFromPath(c.Request.URL.Path)
	if !ok || !host.HasAuthProvider(provider) {
		return false
	}

	ctx := PopulateAuthContext(context.Background(), c)
	baseURL, errBaseURL := h.managementCallbackURL("/v0/management/oauth-callback")
	if errBaseURL != nil {
		log.WithError(errBaseURL).Error("failed to compute plugin auth callback URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	resp, handled, errStart := host.StartLogin(ctx, provider, baseURL)
	if !handled {
		return false
	}
	if errStart != nil {
		log.WithError(errStart).Error("failed to start plugin auth login")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	state := strings.TrimSpace(resp.State)
	if state == "" {
		log.WithField("provider", provider).Error("plugin auth provider returned empty state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errState := ValidateOAuthState(state); errState != nil {
		log.WithError(errState).WithField("provider", provider).Error("plugin auth provider returned invalid state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errRegister := RegisterPluginOAuthSession(state, provider, resp.Metadata); errRegister != nil {
		log.WithError(errRegister).WithField("provider", provider).Error("failed to register plugin oauth session")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": resp.URL, "state": state})
	return true
}
func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}
	if h.authManager == nil {
		h.listAuthFilesFromDisk(c)
		return
	}
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	auths := h.authManager.List()
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if !matchesAuthFileLookup(auth, nameFilter, authIndexFilter) {
			continue
		}
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(200, gin.H{"files": files})
}

func lockedAuthIndex(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return strings.TrimSpace(auth.EnsureIndex())
}

func matchesAuthFileLookup(auth *coreauth.Auth, name string, authIndex string) bool {
	if auth == nil {
		return false
	}
	if name != "" && strings.TrimSpace(auth.ID) != name && strings.TrimSpace(auth.FileName) != name {
		return false
	}
	if authIndex != "" && lockedAuthIndex(auth) != authIndex {
		return false
	}
	return true
}

func (h *Handler) lookupAuthFile(name string, authIndex string) (*coreauth.Auth, bool) {
	name = strings.TrimSpace(name)
	authIndex = strings.TrimSpace(authIndex)
	if h == nil || h.authManager == nil || name == "" {
		return nil, false
	}
	if authIndex == "" {
		if auth, ok := h.authManager.GetByID(name); ok {
			return auth, true
		}
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth != nil && (strings.TrimSpace(auth.FileName) == name || lockedAuthIndex(auth) == name) {
				return auth, true
			}
		}
		return nil, false
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if matchesAuthFileLookup(auth, name, authIndex) {
			return auth, true
		}
	}
	return nil, false
}

// GetAuthFileModels returns the models supported by a specific auth file
func (h *Handler) GetAuthFileModels(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// Try to find auth ID via authManager
	var authID string
	if h.authManager != nil {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name || auth.ID == name {
				authID = auth.ID
				break
			}
		}
	}

	if authID == "" {
		authID = name // fallback to filename as ID
	}

	// Get models from registry
	reg := registry.GetGlobalRegistry()
	models := reg.GetModelsForClient(authID)

	result := make([]gin.H, 0, len(models))
	for _, m := range models {
		entry := gin.H{
			"id": m.ID,
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.OwnedBy != "" {
			entry["owned_by"] = m.OwnedBy
		}
		result = append(result, entry)
	}

	c.JSON(200, gin.H{"models": result})
}

// List auth files from disk when the auth manager is unavailable.
func (h *Handler) listAuthFilesFromDisk(c *gin.Context) {
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
		return
	}
	files := make([]gin.H, 0)
	if authIndexFilter != "" {
		c.JSON(200, gin.H{"files": files})
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if nameFilter != "" && name != nameFilter {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

			// Read file to get type field
			full := filepath.Join(h.cfg.AuthDir, name)
			if data, errRead := os.ReadFile(full); errRead == nil {
				typeValue := gjson.GetBytes(data, "type").String()
				emailValue := gjson.GetBytes(data, "email").String()
				fileData["type"] = typeValue
				fileData["email"] = emailValue
				if projectID := strings.TrimSpace(gjson.GetBytes(data, "project_id").String()); projectID != "" {
					fileData["project_id"] = projectID
				}
				if pv := gjson.GetBytes(data, "priority"); pv.Exists() {
					switch pv.Type {
					case gjson.Number:
						fileData["priority"] = int(pv.Int())
					case gjson.String:
						if parsed, errAtoi := strconv.Atoi(strings.TrimSpace(pv.String())); errAtoi == nil {
							fileData["priority"] = parsed
						}
					}
				}
				if wv := gjson.GetBytes(data, coreauth.AttributeWeight); wv.Exists() {
					var rawWeight string
					switch wv.Type {
					case gjson.Number:
						rawWeight = wv.Raw
					case gjson.String:
						rawWeight = wv.String()
					}
					if rawWeight != "" {
						if weight, errWeight := credentialweight.ParseString(rawWeight); errWeight == nil {
							fileData[coreauth.AttributeWeight] = weight
						}
					}
				}
				if nv := gjson.GetBytes(data, "note"); nv.Exists() && nv.Type == gjson.String {
					if trimmed := strings.TrimSpace(nv.String()); trimmed != "" {
						fileData["note"] = trimmed
					}
				}
				if wv := gjson.GetBytes(data, "websockets"); wv.Exists() {
					switch wv.Type {
					case gjson.True:
						fileData["websockets"] = true
					case gjson.False:
						fileData["websockets"] = false
					case gjson.String:
						if parsed, errParse := strconv.ParseBool(strings.TrimSpace(wv.String())); errParse == nil {
							fileData["websockets"] = parsed
						}
					}
				}
			}

			files = append(files, fileData)
		}
	}
	c.JSON(200, gin.H{"files": files})
}

func (h *Handler) buildAuthFileEntry(auth *coreauth.Auth) gin.H {
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return h.buildAuthFileEntryLocked(auth)
}

func (h *Handler) buildAuthFileEntryLocked(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	entry["success"] = auth.Success
	entry["failed"] = auth.Failed
	entry["recent_requests"] = auth.RecentRequestsSnapshot(time.Now())
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry["project_id"] = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// Hide credentials removed from disk but still lingering in memory.
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		entry["id_token"] = claims
	}
	// Expose priority from Attributes (set by synthesizer from JSON "priority" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry["priority"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			switch v := rawPriority.(type) {
			case float64:
				entry["priority"] = int(v)
			case int:
				entry["priority"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["priority"] = parsed
				}
			}
		}
	}
	// Expose note from Attributes (set by synthesizer from JSON "note" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry["note"] = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(rawNote); trimmed != "" {
				entry["note"] = trimmed
			}
		}
	}
	// Expose Qoder credit usage if available.
	if auth.Provider == "qoder" {
		if storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage); ok && storage != nil && storage.GetUsageInfo() != nil {
			u := storage.GetUsageInfo()
			entry["usage"] = gin.H{
				"used":                   u.UserQuota.Used,
				"total":                  u.UserQuota.Total,
				"remaining":              u.UserQuota.Remaining,
				"percentage":             u.TotalUsagePercentage,
				"unit":                   u.UserQuota.Unit,
				"is_quota_exceeded":      u.IsQuotaExceeded,
				"expires_at":             u.ExpiresAt,
				"org_resource_remaining": u.OrgResourcePackage.Remaining,
			}
		}
	}
	if weight, ok := authWeightValue(auth); ok {
		entry[coreauth.AttributeWeight] = weight
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry["websockets"] = websockets
	}
	return entry
}

func authWeightValue(auth *coreauth.Auth) (int64, bool) {
	if auth == nil {
		return 0, false
	}
	if rawWeight := strings.TrimSpace(authAttribute(auth, coreauth.AttributeWeight)); rawWeight != "" {
		weight, errWeight := credentialweight.ParseString(rawWeight)
		return weight, errWeight == nil
	}
	if auth.Metadata == nil {
		return 0, false
	}
	rawWeight, ok := auth.Metadata[coreauth.AttributeWeight]
	if !ok || rawWeight == nil {
		return 0, false
	}
	weight, errWeight := credentialweight.ParseValue(rawWeight)
	return weight, errWeight == nil
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func extractCodexIDTokenClaims(auth *coreauth.Auth) gin.H {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	idTokenRaw, ok := auth.Metadata["id_token"].(string)
	if !ok {
		return nil
	}
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return nil
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return nil
	}

	result := gin.H{}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); v != "" {
		result["chatgpt_account_id"] = v
	}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); v != "" {
		result["plan_type"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveStart; v != nil {
		result["chatgpt_subscription_active_start"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil; v != nil {
		result["chatgpt_subscription_active_until"] = v
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

// Upload auth file: multipart or raw JSON with ?name=
func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()

	fileHeaders, errMultipart := h.multipartAuthFileHeaders(c)
	if errMultipart != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid multipart form: %v", errMultipart)})
		return
	}
	if len(fileHeaders) == 1 {
		if _, errUpload := h.storeUploadedAuthFile(ctx, fileHeaders[0]); errUpload != nil {
			if errors.Is(errUpload, errAuthFileMustBeJSON) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file must be .json"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": errUpload.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if len(fileHeaders) > 1 {
		uploaded := make([]string, 0, len(fileHeaders))
		failed := make([]gin.H, 0)
		for _, file := range fileHeaders {
			name, errUpload := h.storeUploadedAuthFile(ctx, file)
			if errUpload != nil {
				failureName := ""
				if file != nil {
					failureName = filepath.Base(file.Filename)
				}
				msg := errUpload.Error()
				if errors.Is(errUpload, errAuthFileMustBeJSON) {
					msg = "file must be .json"
				}
				failed = append(failed, gin.H{"name": failureName, "error": msg})
				continue
			}
			uploaded = append(uploaded, name)
		}
		if len(failed) > 0 {
			c.JSON(http.StatusMultiStatus, gin.H{
				"status":   "partial",
				"uploaded": len(uploaded),
				"files":    uploaded,
				"failed":   failed,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "uploaded": len(uploaded), "files": uploaded})
		return
	}
	if c.ContentType() == "multipart/form-data" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	if err = h.writeAuthFile(ctx, filepath.Base(name), data); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			if err = os.Remove(full); err == nil {
				if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
					c.JSON(500, gin.H{"error": errDel.Error()})
					return
				}
				deleted++
				h.removeAuth(ctx, full)
			}
		}
		c.JSON(200, gin.H{"status": "ok", "deleted": deleted})
		return
	}

	names, errNames := requestedAuthFileNamesForDelete(c)
	if errNames != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNames.Error()})
		return
	}
	if len(names) == 0 {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if len(names) == 1 {
		if _, status, errDelete := h.deleteAuthFileByName(ctx, names[0]); errDelete != nil {
			c.JSON(status, gin.H{"error": errDelete.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	deletedFiles := make([]string, 0, len(names))
	failed := make([]gin.H, 0)
	for _, name := range names {
		deletedName, _, errDelete := h.deleteAuthFileByName(ctx, name)
		if errDelete != nil {
			failed = append(failed, gin.H{"name": name, "error": errDelete.Error()})
			continue
		}
		deletedFiles = append(deletedFiles, deletedName)
	}
	if len(failed) > 0 {
		c.JSON(http.StatusMultiStatus, gin.H{
			"status":  "partial",
			"deleted": len(deletedFiles),
			"files":   deletedFiles,
			"failed":  failed,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": len(deletedFiles), "files": deletedFiles})
}

func (h *Handler) multipartAuthFileHeaders(c *gin.Context) ([]*multipart.FileHeader, error) {
	if h == nil || c == nil || c.ContentType() != "multipart/form-data" {
		return nil, nil
	}
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers, nil
}

func (h *Handler) storeUploadedAuthFile(ctx context.Context, file *multipart.FileHeader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("no file uploaded")
	}
	name := filepath.Base(strings.TrimSpace(file.Filename))
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", errAuthFileMustBeJSON
	}
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("failed to read uploaded file: %w", err)
	}
	if err := h.writeAuthFile(ctx, name, data); err != nil {
		return "", err
	}
	return name, nil
}

func (h *Handler) writeAuthFile(ctx context.Context, name string, data []byte) error {
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	auth, err := h.buildAuthFromFileData(dst, data)
	if err != nil {
		return err
	}
	persistData := data
	if metadata := metadataFromAuthFileBytes(data); metadata != nil {
		if normalized, ok := normalizeKiroIDETokenMetadata(metadata); ok {
			normalizedData, errMarshal := json.MarshalIndent(normalized, "", "  ")
			if errMarshal != nil {
				return fmt.Errorf("failed to encode normalized auth file: %w", errMarshal)
			}
			persistData = append(normalizedData, '\n')
		}
	}
	if errWrite := os.WriteFile(dst, persistData, 0o600); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	if err := h.upsertAuthRecord(ctx, auth); err != nil {
		return err
	}
	return nil
}

func metadataFromAuthFileBytes(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}
	return metadata
}

func requestedAuthFileNamesForDelete(c *gin.Context) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	names := uniqueAuthFileNames(c.QueryArray("name"))
	if len(names) > 0 {
		return names, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}

	var objectBody struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if body[0] == '[' {
		var arrayBody []string
		if err := json.Unmarshal(body, &arrayBody); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
		return uniqueAuthFileNames(arrayBody), nil
	}
	if err := json.Unmarshal(body, &objectBody); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	out := make([]string, 0, len(objectBody.Names)+1)
	if strings.TrimSpace(objectBody.Name) != "" {
		out = append(out, objectBody.Name)
	}
	out = append(out, objectBody.Names...)
	return uniqueAuthFileNames(out), nil
}

func uniqueAuthFileNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (h *Handler) deleteAuthFileByName(ctx context.Context, name string) (string, int, error) {
	name = strings.TrimSpace(name)
	if isUnsafeAuthFileName(name) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid name")
	}

	targetPath := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	targetID := ""
	if targetAuth := h.findAuthForDelete(name); targetAuth != nil {
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			return filepath.Base(name), http.StatusConflict, errPluginVirtualAuth
		}
		targetID = strings.TrimSpace(targetAuth.ID)
		if path := strings.TrimSpace(authAttribute(targetAuth, "path")); path != "" {
			targetPath = path
		}
	}
	if !filepath.IsAbs(targetPath) {
		if abs, errAbs := filepath.Abs(targetPath); errAbs == nil {
			targetPath = abs
		}
	}
	if errRemove := os.Remove(targetPath); errRemove != nil {
		if os.IsNotExist(errRemove) {
			return filepath.Base(name), http.StatusNotFound, errAuthFileNotFound
		}
		return filepath.Base(name), http.StatusInternalServerError, fmt.Errorf("failed to remove file: %w", errRemove)
	}
	if errDeleteRecord := h.deleteTokenRecord(ctx, targetPath); errDeleteRecord != nil {
		return filepath.Base(name), http.StatusInternalServerError, errDeleteRecord
	}
	h.removeAuthsForPath(ctx, targetPath, targetID)
	return filepath.Base(name), http.StatusOK, nil
}

func isPluginVirtualSourceDelete(name string, auth *coreauth.Auth) bool {
	if !coreauth.IsPluginVirtualAuth(auth) {
		return true
	}
	sourcePath := strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(auth, "path"))
	}
	if sourcePath == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(strings.TrimSpace(name)), filepath.Base(sourcePath))
}

func (h *Handler) findAuthForDelete(name string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(name); ok {
		return auth
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.FileName) == name {
			return auth
		}
		if filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == name {
			return auth
		}
	}
	return nil
}

func (h *Handler) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	id := path
	if h != nil && h.cfg != nil {
		authDir := strings.TrimSpace(h.cfg.AuthDir)
		if resolvedAuthDir, errResolve := util.ResolveAuthDir(authDir); errResolve == nil && resolvedAuthDir != "" {
			authDir = resolvedAuthDir
		}
		if authDir != "" {
			authDir = filepath.Clean(authDir)
			if !filepath.IsAbs(authDir) {
				if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
					authDir = abs
				}
			}
			if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
				id = rel
			}
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	if h.authManager == nil {
		return nil
	}
	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		return err
	}
	return h.upsertAuthRecord(ctx, auth)
}

func (h *Handler) buildAuthFromFileData(path string, data []byte) (*coreauth.Auth, error) {
	if path == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("invalid auth file: %w", err)
	}
	if normalized, ok := normalizeKiroIDETokenMetadata(metadata); ok {
		metadata = normalized
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	auth := (*coreauth.Auth)(nil)
	if h != nil && h.cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:      h.cfg,
			AuthDir:     h.cfg.AuthDir,
			Now:         time.Now(),
			IDGenerator: synthesizer.NewStableIDGenerator(),
		}
		if generated, errSynthesize := synthesizer.SynthesizeAuthFile(sctx, path, data); errSynthesize == nil && len(generated) > 0 && generated[0] != nil {
			auth = generated[0].Clone()
		}
	}
	if auth == nil {
		auth = &coreauth.Auth{
			ID:       authID,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   path,
				"source": path,
			},
			Metadata:  metadata,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}
	auth.ID = authID
	auth.FileName = filepath.Base(path)
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if h != nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(authID); ok {
			auth.CreatedAt = existing.CreatedAt
			if !hasLastRefresh {
				auth.LastRefreshedAt = existing.LastRefreshedAt
			}
			auth.NextRefreshAfter = existing.NextRefreshAfter
			auth.Runtime = existing.Runtime
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func normalizeKiroIDETokenMetadata(metadata map[string]any) (map[string]any, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	if existingType, _ := metadata["type"].(string); strings.TrimSpace(existingType) != "" {
		return nil, false
	}
	accessToken := strings.TrimSpace(stringFromMetadata(metadata, "accessToken"))
	if accessToken == "" {
		return nil, false
	}
	hasKiroIDEField := false
	for _, key := range []string{"profileArn", "clientIdHash", "startUrl"} {
		if strings.TrimSpace(stringFromMetadata(metadata, key)) != "" {
			hasKiroIDEField = true
			break
		}
	}
	if !hasKiroIDEField {
		return nil, false
	}

	email := strings.TrimSpace(stringFromMetadata(metadata, "email"))
	if email == "" {
		email = kiroauth.ExtractEmailFromJWT(accessToken)
	}
	return map[string]any{
		"type":           "kiro",
		"access_token":   accessToken,
		"refresh_token":  strings.TrimSpace(stringFromMetadata(metadata, "refreshToken")),
		"profile_arn":    strings.TrimSpace(stringFromMetadata(metadata, "profileArn")),
		"expires_at":     strings.TrimSpace(stringFromMetadata(metadata, "expiresAt")),
		"auth_method":    strings.ToLower(strings.TrimSpace(stringFromMetadata(metadata, "authMethod"))),
		"provider":       strings.TrimSpace(stringFromMetadata(metadata, "provider")),
		"client_id":      strings.TrimSpace(stringFromMetadata(metadata, "clientId")),
		"client_secret":  strings.TrimSpace(stringFromMetadata(metadata, "clientSecret")),
		"client_id_hash": strings.TrimSpace(stringFromMetadata(metadata, "clientIdHash")),
		"email":          email,
		"start_url":      strings.TrimSpace(stringFromMetadata(metadata, "startUrl")),
		"region":         strings.TrimSpace(stringFromMetadata(metadata, "region")),
	}, true
}

func stringFromMetadata(metadata map[string]any, key string) string {
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return fmt.Sprint(value)
	}
}

func (h *Handler) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.authManager == nil || auth == nil {
		return nil
	}
	if existing, ok := h.authManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

// PatchAuthFileStatus toggles the disabled state of an auth file
func (h *Handler) PatchAuthFileStatus(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  *bool  `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	authIndex := strings.TrimSpace(req.AuthIndex)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	ctx := c.Request.Context()

	targetAuth, _ := h.lookupAuthFile(name, authIndex)
	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		// Allow status changes only when targeting the source auth file name, matching delete semantics.
		// Expanded virtual project auths still cannot be modified independently.
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
			return
		}
		if errPatch := h.patchPluginVirtualSourceStatus(ctx, targetAuth, *req.Disabled); errPatch != nil {
			status := http.StatusInternalServerError
			if errors.Is(errPatch, errAuthFileNotFound) || os.IsNotExist(errPatch) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": errPatch.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
		return
	}

	if coreauth.IsConfigAPIKeyAuth(targetAuth) {
		h.mu.Lock()
		handled, errToggle := toggleConfigAPIKeyExcludedAll(h.cfg, targetAuth, *req.Disabled)
		if errToggle != nil {
			h.mu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update config api key: %v", errToggle)})
			return
		}
		if !handled {
			h.mu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"error": "config api key entry not found"})
			return
		}
		cfgSnapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
		h.mu.Unlock()
		if !okSnapshot {
			return
		}
		h.reloadConfigAfterManagementSave(ctx, cfgSnapshot)
		if h.tokenStore != nil {
			_ = h.tokenStore.Delete(ctx, targetAuth.ID)
		}
		c.JSON(http.StatusOK, gin.H{
			"status":           "ok",
			"disabled":         *req.Disabled,
			"via":              "config:excluded-models",
			"excluded_pattern": configAPIKeyDisablePattern,
		})
		return
	}

	applyAuthDisabledState(targetAuth, *req.Disabled)
	updatedAuth, err := h.authManager.Update(ctx, targetAuth)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}
	if errHook := h.notifyAuthFilePersisted(ctx, updatedAuth); errHook != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to refresh auth: %v", errHook)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
}

// patchPluginVirtualSourceStatus toggles disabled on a plugin multi-auth source file and all
// runtime auths expanded from it. Virtual project children cannot be toggled independently.
func (h *Handler) patchPluginVirtualSourceStatus(ctx context.Context, targetAuth *coreauth.Auth, disabled bool) error {
	if h == nil || h.authManager == nil || targetAuth == nil {
		return fmt.Errorf("core auth manager unavailable")
	}
	sourcePath := strings.TrimSpace(authAttribute(targetAuth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(targetAuth, "path"))
	}
	if sourcePath == "" {
		return errPluginVirtualAuth
	}
	if errWrite := setSourceAuthFileDisabled(sourcePath, disabled); errWrite != nil {
		if os.IsNotExist(errWrite) {
			return errAuthFileNotFound
		}
		return fmt.Errorf("failed to update source auth file: %w", errWrite)
	}
	now := time.Now()
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !sameAuthFilePath(authAttribute(auth, "path"), sourcePath) &&
			!sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), sourcePath) {
			continue
		}
		applyAuthDisabledState(auth, disabled)
		auth.UpdatedAt = now
		if _, errUpdate := h.authManager.Update(ctx, auth); errUpdate != nil {
			return fmt.Errorf("failed to update auth %s: %w", auth.ID, errUpdate)
		}
	}
	return nil
}

func setSourceAuthFileDisabled(path string, disabled bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("source auth path is empty")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	metadata := make(map[string]any)
	if len(bytes.TrimSpace(data)) > 0 {
		if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
			return fmt.Errorf("invalid auth file: %w", errUnmarshal)
		}
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["disabled"] = disabled
	raw, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return fmt.Errorf("marshal auth file: %w", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return nil
}

func applyAuthDisabledState(auth *coreauth.Auth, disabled bool) {
	if auth == nil {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "disabled via management API"
	} else {
		auth.Status = coreauth.StatusActive
		auth.StatusMessage = ""
	}
	auth.UpdatedAt = time.Now()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = disabled
}

// PatchAuthFileFields updates arbitrary metadata fields of an auth file.
func (h *Handler) PatchAuthFileFields(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req map[string]json.RawMessage
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	nameRaw, ok := req["name"]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var nameValue string
	if err := json.Unmarshal(nameRaw, &nameValue); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	name := strings.TrimSpace(nameValue)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	delete(req, "name")

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
		return
	}

	changed := false
	touchedRoots := make(map[string]struct{}, len(req))
	for key, rawValue := range req {
		fieldPath := strings.TrimSpace(key)
		if fieldPath == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "field name is required"})
			return
		}
		value, errDecode := decodeAuthFileFieldValue(rawValue)
		if errDecode != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid field %s", fieldPath)})
			return
		}
		if targetAuth.Metadata == nil {
			targetAuth.Metadata = make(map[string]any)
		}

		if fieldPath == coreauth.AttributeWeight {
			if value == nil {
				delete(targetAuth.Metadata, coreauth.AttributeWeight)
			} else {
				if _, okNumber := value.(json.Number); !okNumber {
					c.JSON(http.StatusBadRequest, gin.H{"error": "weight must be an integer"})
					return
				}
				weight, errWeight := credentialweight.ParseValue(value)
				if errWeight != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": errWeight.Error()})
					return
				}
				targetAuth.Metadata[coreauth.AttributeWeight] = weight
			}
		} else if rootAuthFileField(fieldPath) == coreauth.AttributeWeight {
			c.JSON(http.StatusBadRequest, gin.H{"error": "weight does not support nested fields"})
			return
		} else if fieldPath == "headers" {
			applyAuthFileHeadersPatch(targetAuth, value)
		} else if errSet := setAuthFileMetadataValue(targetAuth.Metadata, fieldPath, value); errSet != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errSet.Error()})
			return
		}
		if root := rootAuthFileField(fieldPath); root != "" {
			touchedRoots[root] = struct{}{}
		}
		changed = true
	}
	if changed {
		syncAuthFileMetadataFields(targetAuth, touchedRoots)
	}

	if !changed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	targetAuth.UpdatedAt = time.Now()

	updatedAuth, err := h.authManager.Update(ctx, targetAuth)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}
	if errHook := h.notifyAuthFilePersisted(ctx, updatedAuth); errHook != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to refresh auth: %v", errHook)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) notifyAuthFilePersisted(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.postAuthPersistHook == nil || auth == nil {
		return nil
	}
	return h.postAuthPersistHook(ctx, auth)
}

func decodeAuthFileFieldValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func rootAuthFileField(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "."); idx >= 0 {
		return strings.TrimSpace(path[:idx])
	}
	return path
}

func setAuthFileMetadataValue(metadata map[string]any, path string, value any) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	parts := strings.Split(path, ".")
	current := metadata
	for i, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return fmt.Errorf("invalid field path: %s", path)
		}
		if i == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	return nil
}

func applyAuthFileHeadersPatch(auth *coreauth.Auth, value any) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	headersPatch, ok := authFileHeadersStringMap(value)
	if !ok {
		auth.Metadata["headers"] = value
		return
	}

	existingHeaders := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata)
	nextHeaders := make(map[string]string, len(existingHeaders))
	for key, val := range existingHeaders {
		nextHeaders[key] = val
	}
	for key, value := range headersPatch {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		val := strings.TrimSpace(value)
		if val == "" {
			delete(nextHeaders, name)
			continue
		}
		nextHeaders[name] = val
	}

	if len(nextHeaders) == 0 {
		delete(auth.Metadata, "headers")
		return
	}
	metaHeaders := make(map[string]any, len(nextHeaders))
	for key, value := range nextHeaders {
		metaHeaders[key] = value
	}
	auth.Metadata["headers"] = metaHeaders
}

func authFileHeadersStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, rawValue := range typed {
			value, ok := rawValue.(string)
			if !ok {
				return nil, false
			}
			out[key] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func syncAuthFileMetadataFields(auth *coreauth.Auth, touchedRoots map[string]struct{}) {
	if auth == nil || len(touchedRoots) == 0 {
		return
	}
	if _, ok := touchedRoots["prefix"]; ok {
		if prefix, okString := auth.Metadata["prefix"].(string); okString {
			auth.Prefix = strings.TrimSpace(prefix)
		}
	}
	if _, ok := touchedRoots["proxy_url"]; ok {
		if proxyURL, okString := auth.Metadata["proxy_url"].(string); okString {
			auth.ProxyURL = strings.TrimSpace(proxyURL)
		}
	}
	if _, ok := touchedRoots["headers"]; ok {
		syncAuthFileHeaderAttributes(auth)
	}
	if _, ok := touchedRoots["priority"]; ok {
		syncAuthFilePriorityAttribute(auth)
	}
	if _, ok := touchedRoots[coreauth.AttributeWeight]; ok {
		syncAuthFileWeightAttribute(auth)
	}
	if _, ok := touchedRoots["note"]; ok {
		syncAuthFileNoteAttribute(auth)
	}
	if _, ok := touchedRoots["websockets"]; ok {
		syncAuthFileWebsocketsAttribute(auth)
	}
	if _, ok := touchedRoots["disabled"]; ok {
		syncAuthFileDisabledState(auth)
	}
	if _, ok := touchedRoots["excluded_models"]; ok {
		syncAuthFileExcludedModelsAttribute(auth, touchedRoots)
	} else if _, ok := touchedRoots["excluded-models"]; ok {
		syncAuthFileExcludedModelsAttribute(auth, touchedRoots)
	}
}

func syncAuthFileHeaderAttributes(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	for key := range auth.Attributes {
		if strings.HasPrefix(key, "header:") {
			delete(auth.Attributes, key)
		}
	}
	for name, value := range coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata) {
		auth.Attributes["header:"+name] = value
	}
}

func syncAuthFilePriorityAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	priority, ok := authFileIntValue(auth.Metadata["priority"])
	if !ok {
		delete(auth.Attributes, "priority")
		return
	}
	if priority == 0 {
		delete(auth.Attributes, "priority")
		return
	}
	auth.Attributes["priority"] = strconv.Itoa(priority)
}

func syncAuthFileWeightAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	weight, errWeight := credentialweight.ParseValue(auth.Metadata[coreauth.AttributeWeight])
	if errWeight != nil {
		delete(auth.Attributes, coreauth.AttributeWeight)
		return
	}
	auth.Attributes[coreauth.AttributeWeight] = strconv.FormatInt(weight, 10)
}

func authFileIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func syncAuthFileNoteAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	note, ok := auth.Metadata["note"].(string)
	if !ok {
		delete(auth.Attributes, "note")
		return
	}
	note = strings.TrimSpace(note)
	if note == "" {
		delete(auth.Attributes, "note")
		return
	}
	auth.Attributes["note"] = note
}

func syncAuthFileWebsocketsAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	websockets, ok := authFileBoolValue(auth.Metadata["websockets"])
	if !ok {
		delete(auth.Attributes, "websockets")
		return
	}
	auth.Attributes["websockets"] = strconv.FormatBool(websockets)
}

func syncAuthFileExcludedModelsAttribute(auth *coreauth.Auth, touchedRoots map[string]struct{}) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	sourceKey := "excluded_models"
	if _, okUnderscore := touchedRoots["excluded_models"]; !okUnderscore {
		if _, okHyphen := touchedRoots["excluded-models"]; okHyphen {
			sourceKey = "excluded-models"
		}
	}
	excluded := authFileExcludedModelsValue(auth.Metadata[sourceKey])
	excluded = internalconfig.NormalizeExcludedModels(excluded)
	delete(auth.Metadata, "excluded-models")
	if len(excluded) == 0 {
		delete(auth.Metadata, "excluded_models")
		delete(auth.Attributes, "excluded_models")
		delete(auth.Attributes, "excluded_models_hash")
		return
	}
	auth.Metadata["excluded_models"] = append([]string(nil), excluded...)
	auth.Attributes["excluded_models"] = strings.Join(excluded, ",")
	auth.Attributes["excluded_models_hash"] = diff.ComputeExcludedModelsHash(excluded)
}

func authFileExcludedModelsValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	case string:
		return strings.Split(typed, ",")
	default:
		return nil
	}
}

func authFileBoolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func syncAuthFileDisabledState(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	disabled, ok := authFileBoolValue(auth.Metadata["disabled"])
	if !ok {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		if strings.TrimSpace(auth.StatusMessage) == "" {
			auth.StatusMessage = "disabled via management API"
		}
		return
	}
	auth.Status = coreauth.StatusActive
	auth.StatusMessage = ""
}

func (h *Handler) removeAuth(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if _, ok := h.authManager.GetByID(id); ok {
		h.authManager.Remove(ctx, id)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	h.authManager.Remove(ctx, authID)
}

func (h *Handler) removeAuthsForPath(ctx context.Context, path string, fallbackID string) {
	if h == nil || h.authManager == nil {
		return
	}
	removed := false
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if sameAuthFilePath(authAttribute(auth, "path"), path) || sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), path) {
			h.removeAuth(ctx, auth.ID)
			removed = true
		}
	}
	if removed {
		return
	}
	if strings.TrimSpace(fallbackID) != "" {
		h.removeAuth(ctx, fallbackID)
		return
	}
	h.removeAuth(ctx, path)
}

func sameAuthFilePath(left, right string) bool {
	left = cleanAuthFilePath(left)
	right = cleanAuthFilePath(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func cleanAuthFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}

func (h *Handler) deleteTokenRecord(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path is empty")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	return store.Delete(ctx, path)
}

func (h *Handler) tokenStoreWithBaseDir() coreauth.Store {
	if h == nil {
		return nil
	}
	store := h.tokenStore
	if store == nil {
		store = sdkAuth.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	h.mergeExistingAuthFileMetadata(record)
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return "", fmt.Errorf("token store unavailable")
	}
	if h.postAuthHook != nil {
		if err := h.postAuthHook(ctx, record); err != nil {
			return "", fmt.Errorf("post-auth hook failed: %w", err)
		}
	}
	savedPath, errSave := store.Save(ctx, record)
	if errSave != nil {
		return savedPath, errSave
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(ctx, record); errHook != nil {
			return savedPath, fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	return savedPath, nil
}

func (h *Handler) mergeExistingAuthFileMetadata(record *coreauth.Auth) {
	if h == nil || record == nil {
		return
	}
	var existingMap map[string]any
	if h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
		targetFile := record.FileName
		if targetFile == "" {
			targetFile = record.ID
		}
		if targetFile != "" {
			fullPath := filepath.Join(h.cfg.AuthDir, targetFile)
			if raw, errRead := os.ReadFile(fullPath); errRead == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &existingMap)
			}
		}
	}
	if existingMap == nil && h.authManager != nil {
		if existing, ok := h.authManager.GetByID(record.ID); ok && existing != nil && existing.Metadata != nil {
			existingMap = existing.Metadata
		} else {
			for _, auth := range h.authManager.List() {
				if auth != nil && auth.FileName == record.FileName && auth.Metadata != nil {
					existingMap = auth.Metadata
					break
				}
			}
		}
	}
	if len(existingMap) > 0 {
		coreauth.MergeExistingAuthMetadata(record, existingMap)
	}
}

func (h *Handler) saveOAuthTokenRecord(ctx context.Context, state, provider string, record *coreauth.Auth) (string, error) {
	if errBegin := beginOAuthSessionSave(state, provider); errBegin != nil {
		return "", errBegin
	}
	return h.saveTokenRecord(ctx, record)
}

func gitLabBaseURLFromRequest(c *gin.Context) string {
	if c != nil {
		if raw := strings.TrimSpace(c.Query("base_url")); raw != "" {
			return gitlabauth.NormalizeBaseURL(raw)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GITLAB_BASE_URL")); raw != "" {
		return gitlabauth.NormalizeBaseURL(raw)
	}
	return gitlabauth.DefaultBaseURL
}

func buildGitLabAuthMetadata(baseURL, mode string, tokenResp *gitlabauth.TokenResponse, direct *gitlabauth.DirectAccessResponse) map[string]any {
	metadata := map[string]any{
		"type":                     "gitlab",
		"auth_method":              strings.TrimSpace(mode),
		"base_url":                 gitlabauth.NormalizeBaseURL(baseURL),
		"last_refresh":             time.Now().UTC().Format(time.RFC3339),
		"refresh_interval_seconds": 240,
	}
	if tokenResp != nil {
		metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
		if refreshToken := strings.TrimSpace(tokenResp.RefreshToken); refreshToken != "" {
			metadata["refresh_token"] = refreshToken
		}
		if tokenType := strings.TrimSpace(tokenResp.TokenType); tokenType != "" {
			metadata["token_type"] = tokenType
		}
		if scope := strings.TrimSpace(tokenResp.Scope); scope != "" {
			metadata["scope"] = scope
		}
		if expiry := gitlabauth.TokenExpiry(time.Now(), tokenResp); !expiry.IsZero() {
			metadata["oauth_expires_at"] = expiry.Format(time.RFC3339)
		}
	}
	mergeGitLabDirectAccessMetadata(metadata, direct)
	return metadata
}

func mergeGitLabDirectAccessMetadata(metadata map[string]any, direct *gitlabauth.DirectAccessResponse) {
	if metadata == nil || direct == nil {
		return
	}
	if base := strings.TrimSpace(direct.BaseURL); base != "" {
		metadata["duo_gateway_base_url"] = base
	}
	if token := strings.TrimSpace(direct.Token); token != "" {
		metadata["duo_gateway_token"] = token
	}
	if direct.ExpiresAt > 0 {
		expiry := time.Unix(direct.ExpiresAt, 0).UTC()
		metadata["duo_gateway_expires_at"] = expiry.Format(time.RFC3339)
		now := time.Now().UTC()
		if ttl := expiry.Sub(now); ttl > 0 {
			interval := int(ttl.Seconds()) / 2
			switch {
			case interval < 60:
				interval = 60
			case interval > 240:
				interval = 240
			}
			metadata["refresh_interval_seconds"] = interval
		}
	}
	if len(direct.Headers) > 0 {
		headers := make(map[string]string, len(direct.Headers))
		for key, value := range direct.Headers {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			headers[key] = value
		}
		if len(headers) > 0 {
			metadata["duo_gateway_headers"] = headers
		}
	}
	if direct.ModelDetails != nil {
		modelDetails := map[string]any{}
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			modelDetails["model_provider"] = provider
			metadata["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			modelDetails["model_name"] = model
			metadata["model_name"] = model
		}
		if len(modelDetails) > 0 {
			metadata["model_details"] = modelDetails
		}
	}
}

func primaryGitLabEmail(user *gitlabauth.User) string {
	if user == nil {
		return ""
	}
	if value := strings.TrimSpace(user.Email); value != "" {
		return value
	}
	return strings.TrimSpace(user.PublicEmail)
}

func gitLabAccountIdentifier(user *gitlabauth.User) string {
	if user == nil {
		return "user"
	}
	for _, value := range []string{user.Username, primaryGitLabEmail(user), user.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "user"
}

func sanitizeGitLabFileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "user"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "user"
	}
	return result
}

func maskGitLabToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(h.cfg)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "anthropic")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(anthropicCallbackPort, forwarder)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if !IsOAuthSessionPending(state, "anthropic") {
					return nil, errOAuthSessionNotPending
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			SetOAuthSessionError(state, "Bad request")
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			SetOAuthSessionError(state, "State code error")
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens using internal auth service
		bundle, errExchange := anthropicAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
		if errExchange != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchange)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: map[string]any{"email": tokenStorage.Email},
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "anthropic", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Codex auth service
	openaiAuth := newCodexOAuthService(h.cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "codex")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/codex/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(codexCallbackPort, "codex", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(codexCallbackPort, forwarder)
		}

		// Wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "codex") {
				return
			}
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					SetOAuthSessionError(state, "Bad Request")
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					SetOAuthSessionError(state, "State code error")
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Exchange code for tokens using internal auth service
		bundle, errExchange := openaiAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
		if errExchange != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchange)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange))
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		hashAccountID := ""
		if claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "codex", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitLabToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing GitLab Duo authentication...")

	baseURL := gitLabBaseURLFromRequest(c)
	clientID := strings.TrimSpace(c.Query("client_id"))
	clientSecret := strings.TrimSpace(c.Query("client_secret"))
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("GITLAB_OAUTH_CLIENT_ID"))
	}
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv("GITLAB_OAUTH_CLIENT_SECRET"))
	}
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gitlab client_id is required"})
		return
	}

	pkceCodes, err := gitlabauth.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate GitLab PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate GitLab state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := gitlabauth.RedirectURL(gitlabauth.DefaultCallbackPort)
	authClient := gitlabauth.NewAuthClient(h.cfg)
	authURL, err := authClient.GenerateAuthURL(baseURL, clientID, redirectURI, state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate GitLab authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "gitlab")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/gitlab/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute gitlab callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(gitlabauth.DefaultCallbackPort, "gitlab", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start gitlab callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(gitlabauth.DefaultCallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-gitlab-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "gitlab") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("gitlab oauth flow timed out")
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errRead := os.ReadFile(waitFile); errRead == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					SetOAuthSessionError(state, errStr)
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != state {
					SetOAuthSessionError(state, "State code error")
					return
				}
				code = strings.TrimSpace(payload["code"])
				if code == "" {
					SetOAuthSessionError(state, "Authorization code missing")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errExchange := authClient.ExchangeCodeForTokens(ctx, baseURL, clientID, clientSecret, redirectURI, code, pkceCodes.CodeVerifier)
		if errExchange != nil {
			log.Errorf("Failed to exchange GitLab authorization code: %v", errExchange)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		user, errUser := authClient.GetCurrentUser(ctx, baseURL, tokenResp.AccessToken)
		if errUser != nil {
			log.Errorf("Failed to fetch GitLab user profile: %v", errUser)
			SetOAuthSessionError(state, "Failed to fetch account profile")
			return
		}

		direct, errDirect := authClient.FetchDirectAccess(ctx, baseURL, tokenResp.AccessToken)
		if errDirect != nil {
			log.Errorf("Failed to fetch GitLab direct access metadata: %v", errDirect)
			SetOAuthSessionError(state, "Failed to fetch GitLab Duo access")
			return
		}

		identifier := gitLabAccountIdentifier(user)
		fileName := fmt.Sprintf("gitlab-%s.json", sanitizeGitLabFileName(identifier))
		metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModeOAuth, tokenResp, direct)
		metadata["auth_kind"] = "oauth"
		metadata["oauth_client_id"] = clientID
		metadata["username"] = strings.TrimSpace(user.Username)
		if email := primaryGitLabEmail(user); email != "" {
			metadata["email"] = email
		}
		metadata["name"] = strings.TrimSpace(user.Name)

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "gitlab",
			FileName: fileName,
			Label:    identifier,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "gitlab", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save GitLab auth record: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("GitLab Duo authentication successful. Token saved to %s\n", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("gitlab")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitLabPATToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var payload struct {
		BaseURL             string `json:"base_url"`
		PersonalAccessToken string `json:"personal_access_token"`
		Token               string `json:"token"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	baseURL := gitlabauth.NormalizeBaseURL(strings.TrimSpace(payload.BaseURL))
	if baseURL == "" {
		baseURL = gitLabBaseURLFromRequest(nil)
	}
	pat := strings.TrimSpace(payload.PersonalAccessToken)
	if pat == "" {
		pat = strings.TrimSpace(payload.Token)
	}
	if pat == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "personal_access_token is required"})
		return
	}

	authClient := gitlabauth.NewAuthClient(h.cfg)

	user, err := authClient.GetCurrentUser(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}
	patSelf, err := authClient.GetPersonalAccessTokenSelf(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}
	direct, err := authClient.FetchDirectAccess(ctx, baseURL, pat)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	identifier := gitLabAccountIdentifier(user)
	fileName := fmt.Sprintf("gitlab-%s-pat.json", sanitizeGitLabFileName(identifier))
	metadata := buildGitLabAuthMetadata(baseURL, gitLabLoginModePAT, nil, direct)
	metadata["auth_kind"] = "personal_access_token"
	metadata["personal_access_token"] = pat
	metadata["token_preview"] = maskGitLabToken(pat)
	metadata["username"] = strings.TrimSpace(user.Username)
	if email := primaryGitLabEmail(user); email != "" {
		metadata["email"] = email
	}
	metadata["name"] = strings.TrimSpace(user.Name)
	if patSelf != nil {
		if name := strings.TrimSpace(patSelf.Name); name != "" {
			metadata["pat_name"] = name
		}
		if len(patSelf.Scopes) > 0 {
			metadata["pat_scopes"] = append([]string(nil), patSelf.Scopes...)
		}
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "gitlab",
		FileName: fileName,
		Label:    identifier + " (PAT)",
		Metadata: metadata,
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	response := gin.H{
		"status":      "ok",
		"saved_path":  savedPath,
		"username":    strings.TrimSpace(user.Username),
		"email":       primaryGitLabEmail(user),
		"token_label": identifier,
	}
	if direct != nil && direct.ModelDetails != nil {
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			response["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			response["model_name"] = model
		}
	}

	fmt.Printf("GitLab Duo PAT authentication successful. Token saved to %s\n", savedPath)
	c.JSON(http.StatusOK, response)
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Antigravity authentication...")

	authSvc := antigravity.NewAntigravityAuth(h.cfg, nil)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravity.CallbackPort)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	RegisterOAuthSession(state, "antigravity")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/antigravity/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute antigravity callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(antigravity.CallbackPort, "antigravity", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start antigravity callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(antigravity.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "antigravity") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errReadFile := os.ReadFile(waitFile); errReadFile == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != "" && payloadState != state {
					log.Errorf("Authentication failed: state mismatch")
					SetOAuthSessionError(state, "Authentication failed: state mismatch")
					return
				}
				authCode = strings.TrimSpace(payload["code"])
				if authCode == "" {
					log.Error("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, authCode, redirectURI)
		if errToken != nil {
			log.Errorf("Failed to exchange token: %v", errToken)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		if accessToken == "" {
			log.Error("antigravity: token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
		if errInfo != nil {
			log.Errorf("Failed to fetch user info: %v", errInfo)
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}
		email = strings.TrimSpace(email)
		if email == "" {
			log.Error("antigravity: user info returned empty email")
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}

		projectID := ""
		if accessToken != "" {
			fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
			if errProject != nil {
				log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
			} else {
				projectID = fetchedProjectID
				log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
			}
		}

		now := time.Now()
		metadata := map[string]any{
			"type":          "antigravity",
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}

		fileName := antigravity.CredentialFileName(email)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "antigravity"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "antigravity",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "antigravity", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if projectID != "" {
			fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
		}
		fmt.Println("You can now use Antigravity services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestXAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing xAI authentication...")

	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuth(h.cfg)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSession(state, "xai")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "xai")

		fmt.Println("Waiting for xAI authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "xai") {
				return
			}
			log.Errorf("xAI authentication failed: %v", errWaitForAuthorization)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			return
		}
		if !IsOAuthSessionPending(state, "xai") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("xAI token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
		label := strings.TrimSpace(tokenStorage.Email)
		if label == "" {
			label = "xAI"
		}

		metadata := map[string]any{
			"type":           "xai",
			"access_token":   tokenStorage.AccessToken,
			"refresh_token":  tokenStorage.RefreshToken,
			"id_token":       tokenStorage.IDToken,
			"token_type":     tokenStorage.TokenType,
			"expires_in":     tokenStorage.ExpiresIn,
			"expired":        tokenStorage.Expire,
			"last_refresh":   tokenStorage.LastRefresh,
			"base_url":       tokenStorage.BaseURL,
			"token_endpoint": tokenStorage.TokenEndpoint,
			"auth_kind":      "oauth",
		}
		if tokenStorage.Email != "" {
			metadata["email"] = tokenStorage.Email
		}
		if tokenStorage.Subject != "" {
			metadata["sub"] = tokenStorage.Subject
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "xai",
			FileName: fileName,
			Label:    label,
			Storage:  tokenStorage,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  tokenStorage.BaseURL,
			},
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "xai", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use xAI services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(xaiauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

func (h *Handler) RequestQoderToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Qoder authentication...")

	state := fmt.Sprintf("qod-%d", time.Now().UnixNano())
	qoderAuth := qoderauth.NewQoderAuth(h.cfg)

	deviceFlow, err := qoderAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "qoder")

	go func() {
		fmt.Println("Waiting for authentication...")
		tokenData, errPollForToken := qoderAuth.PollForToken(ctx, deviceFlow)
		if errPollForToken != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errPollForToken)
			return
		}

		storage := qoderAuth.CreateTokenStorage(tokenData, deviceFlow.MachineID)
		// Resolve a human-readable label: prefer the email from /userinfo,
		// fall back to user_id, then to a timestamp so the auth file always
		// gets a unique, non-empty name without prompting the operator.
		name, email := qoderAuth.SaveUserInfo(ctx, tokenData.AccessToken, tokenData.UserID, "", "")
		storage.Name = name
		switch {
		case strings.TrimSpace(email) != "":
			storage.Email = strings.TrimSpace(email)
		case strings.TrimSpace(tokenData.UserID) != "":
			storage.Email = strings.TrimSpace(tokenData.UserID)
		default:
			storage.Email = fmt.Sprintf("user-%d", time.Now().UnixMilli())
		}
		fileName := fmt.Sprintf("qoder-%s.json", storage.Email)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "qoder",
			FileName: fileName,
			Label: func() string {
				if storage.Name != "" {
					return storage.Name
				}
				if storage.Email != "" {
					return storage.Email
				}
				return "Qoder User"
			}(),
			Storage:  storage,
			Metadata: map[string]any{"email": storage.Email},
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "qoder", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Qoder services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("qoder")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": deviceFlow.VerificationURIComplete, "state": state})
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Kimi authentication...")

	state := fmt.Sprintf("kmi-%d", time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuth(h.cfg)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, "kimi")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kimi")

		fmt.Println("Waiting for authentication...")
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "kimi") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			fmt.Printf("Authentication failed: %v\n", errWaitForAuthorization)
			return
		}
		if !IsOAuthSessionPending(state, "kimi") {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)

		metadata := map[string]any{
			"type":          "kimi",
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("kimi-%d.json", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kimi",
			FileName: fileName,
			Label:    "Kimi User",
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "kimi", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kimi services through this CLI")
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	}
	c.JSON(200, response)
}

// watchOAuthSessionCancel cancels pollCtx once the OAuth session is no longer pending.
func watchOAuthSessionCancel(pollCtx context.Context, cancel context.CancelFunc, state, provider string) {
	if cancel == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
			if !IsOAuthSessionPending(state, provider) {
				cancel()
				return
			}
		}
	}
}

// CancelAuthSession cancels a pending OAuth session identified by state.
// Protected by management auth. Safe for both callback and device-code flows:
// waiters check IsOAuthSessionPending and exit without saving credentials.
func (h *Handler) CancelAuthSession(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing state"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	cancelled := CancelOAuthSession(state)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cancelled": cancelled})
}

func (h *Handler) RequestIFlowToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing iFlow authentication...")

	state := fmt.Sprintf("ifl-%d", time.Now().UnixNano())
	authSvc := iflowauth.NewIFlowAuth(h.cfg)
	authURL, redirectURI := authSvc.AuthorizationURL(state, iflowauth.CallbackPort)

	RegisterOAuthSession(state, "iflow")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/iflow/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute iflow callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(iflowauth.CallbackPort, "iflow", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start iflow callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(iflowauth.CallbackPort, forwarder)
		}
		fmt.Println("Waiting for authentication...")

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-iflow-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var resultMap map[string]string
		for {
			if !IsOAuthSessionPending(state, "iflow") {
				return
			}
			if time.Now().After(deadline) {
				SetOAuthSessionError(state, "Authentication failed")
				fmt.Println("Authentication failed: timeout waiting for callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				_ = os.Remove(waitFile)
				_ = json.Unmarshal(data, &resultMap)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if errStr := strings.TrimSpace(resultMap["error"]); errStr != "" {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %s\n", errStr)
			return
		}
		if resultState := strings.TrimSpace(resultMap["state"]); resultState != state {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Println("Authentication failed: state mismatch")
			return
		}

		code := strings.TrimSpace(resultMap["code"])
		if code == "" {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Println("Authentication failed: code missing")
			return
		}

		tokenData, errExchange := authSvc.ExchangeCodeForTokens(ctx, code, redirectURI)
		if errExchange != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errExchange)
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(tokenData)
		identifier := strings.TrimSpace(tokenStorage.Email)
		if identifier == "" {
			identifier = fmt.Sprintf("%d", time.Now().UnixMilli())
			tokenStorage.Email = identifier
		}
		record := &coreauth.Auth{
			ID:         fmt.Sprintf("iflow-%s.json", identifier),
			Provider:   "iflow",
			FileName:   fmt.Sprintf("iflow-%s.json", identifier),
			Storage:    tokenStorage,
			Metadata:   map[string]any{"email": identifier, "api_key": tokenStorage.APIKey},
			Attributes: map[string]string{"api_key": tokenStorage.APIKey},
		}

		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "iflow", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if tokenStorage.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use iFlow services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("iflow")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestGitHubToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing GitHub Copilot authentication...")

	state := fmt.Sprintf("gh-%d", time.Now().UnixNano())

	// Initialize Copilot auth service
	deviceClient := copilot.NewDeviceFlowClient(h.cfg)

	// Initiate device flow
	deviceCode, err := deviceClient.RequestDeviceCode(ctx)
	if err != nil {
		log.Errorf("Failed to initiate device flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
		return
	}

	authURL := deviceCode.VerificationURI
	userCode := deviceCode.UserCode

	RegisterOAuthSession(state, "github-copilot")

	go func() {
		fmt.Printf("Please visit %s and enter code: %s\n", authURL, userCode)

		tokenData, errPoll := deviceClient.PollForToken(ctx, deviceCode)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errPoll)
			return
		}

		userInfo, errUser := deviceClient.FetchUserInfo(ctx, tokenData.AccessToken)
		if errUser != nil {
			log.Warnf("Failed to fetch user info: %v", errUser)
		}

		username := userInfo.Login
		if username == "" {
			username = "github-user"
		}

		tokenStorage := &copilot.CopilotTokenStorage{
			AccessToken: tokenData.AccessToken,
			TokenType:   tokenData.TokenType,
			Scope:       tokenData.Scope,
			Username:    username,
			Email:       userInfo.Email,
			Name:        userInfo.Name,
			Type:        "github-copilot",
		}

		fileName := fmt.Sprintf("github-copilot-%s.json", username)
		label := userInfo.Email
		if label == "" {
			label = username
		}
		metadata, errMeta := copilotTokenMetadata(tokenStorage)
		if errMeta != nil {
			log.Errorf("Failed to build token metadata: %v", errMeta)
			SetOAuthSessionError(state, "Failed to build token metadata")
			return
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "github-copilot",
			Label:    label,
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: metadata,
		}

		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "github-copilot", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use GitHub Copilot services through this CLI")
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("github-copilot")
	}()

	c.JSON(200, gin.H{
		"status":           "ok",
		"url":              authURL,
		"state":            state,
		"user_code":        userCode,
		"verification_uri": authURL,
	})
}

func copilotTokenMetadata(storage *copilot.CopilotTokenStorage) (map[string]any, error) {
	if storage == nil {
		return nil, fmt.Errorf("token storage is nil")
	}
	payload, errMarshal := json.Marshal(storage)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal token storage: %w", errMarshal)
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(payload, &metadata); errUnmarshal != nil {
		return nil, fmt.Errorf("unmarshal token storage: %w", errUnmarshal)
	}
	return metadata, nil
}

func (h *Handler) RequestIFlowCookieToken(c *gin.Context) {
	ctx := context.Background()

	var payload struct {
		Cookie string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "cookie is required"})
		return
	}

	cookieValue := strings.TrimSpace(payload.Cookie)

	if cookieValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "cookie is required"})
		return
	}

	cookieValue, errNormalize := iflowauth.NormalizeCookie(cookieValue)
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errNormalize.Error()})
		return
	}

	// Check for duplicate BXAuth before authentication
	bxAuth := iflowauth.ExtractBXAuth(cookieValue)
	if existingFile, err := iflowauth.CheckDuplicateBXAuth(h.cfg.AuthDir, bxAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to check duplicate"})
		return
	} else if existingFile != "" {
		existingFileName := filepath.Base(existingFile)
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "duplicate BXAuth found", "existing_file": existingFileName})
		return
	}

	authSvc := iflowauth.NewIFlowAuth(h.cfg)
	tokenData, errAuth := authSvc.AuthenticateWithCookie(ctx, cookieValue)
	if errAuth != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errAuth.Error()})
		return
	}

	tokenData.Cookie = cookieValue

	tokenStorage := authSvc.CreateCookieTokenStorage(tokenData)
	email := strings.TrimSpace(tokenStorage.Email)
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "failed to extract email from token"})
		return
	}

	fileName := iflowauth.SanitizeIFlowFileName(email)
	if fileName == "" {
		fileName = fmt.Sprintf("iflow-%d", time.Now().UnixMilli())
	} else {
		fileName = fmt.Sprintf("iflow-%s", fileName)
	}

	tokenStorage.Email = email
	timestamp := time.Now().Unix()

	record := &coreauth.Auth{
		ID:       fmt.Sprintf("%s-%d.json", fileName, timestamp),
		Provider: "iflow",
		FileName: fmt.Sprintf("%s-%d.json", fileName, timestamp),
		Storage:  tokenStorage,
		Metadata: map[string]any{
			"email":        email,
			"api_key":      tokenStorage.APIKey,
			"expired":      tokenStorage.Expire,
			"cookie":       tokenStorage.Cookie,
			"type":         tokenStorage.Type,
			"last_refresh": tokenStorage.LastRefresh,
		},
		Attributes: map[string]string{
			"api_key": tokenStorage.APIKey,
		},
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	fmt.Printf("iFlow cookie authentication successful. Token saved to %s\n", savedPath)
	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"saved_path": savedPath,
		"email":      email,
		"expired":    tokenStorage.Expire,
		"type":       tokenStorage.Type,
	})
}

type projectSelectionRequiredError struct{}

func (e *projectSelectionRequiredError) Error() string {
	return "gemini cli: project selection required"
}

func ensureGeminiProjectAndOnboard(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage, requestedProject string) error {
	if storage == nil {
		return fmt.Errorf("gemini storage is nil")
	}

	trimmedRequest := strings.TrimSpace(requestedProject)
	if trimmedRequest == "" {
		projects, errProjects := fetchGCPProjects(ctx, httpClient)
		if errProjects != nil {
			return fmt.Errorf("fetch project list: %w", errProjects)
		}
		if len(projects) == 0 {
			return fmt.Errorf("no Google Cloud projects available for this account")
		}
		trimmedRequest = strings.TrimSpace(projects[0].ProjectID)
		if trimmedRequest == "" {
			return fmt.Errorf("resolved project id is empty")
		}
		storage.Auto = true
	} else {
		storage.Auto = false
	}

	if err := performGeminiCLISetup(ctx, httpClient, storage, trimmedRequest); err != nil {
		return err
	}

	if strings.TrimSpace(storage.ProjectID) == "" {
		storage.ProjectID = trimmedRequest
	}

	return nil
}

func onboardAllGeminiProjects(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage) ([]string, error) {
	projects, errProjects := fetchGCPProjects(ctx, httpClient)
	if errProjects != nil {
		return nil, fmt.Errorf("fetch project list: %w", errProjects)
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("no Google Cloud projects available for this account")
	}
	activated := make([]string, 0, len(projects))
	seen := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		candidate := strings.TrimSpace(project.ProjectID)
		if candidate == "" {
			continue
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		if err := performGeminiCLISetup(ctx, httpClient, storage, candidate); err != nil {
			return nil, fmt.Errorf("onboard project %s: %w", candidate, err)
		}
		finalID := strings.TrimSpace(storage.ProjectID)
		if finalID == "" {
			finalID = candidate
		}
		activated = append(activated, finalID)
		seen[candidate] = struct{}{}
	}
	if len(activated) == 0 {
		return nil, fmt.Errorf("no Google Cloud projects available for this account")
	}
	return activated, nil
}

func ensureGeminiProjectsEnabled(ctx context.Context, httpClient *http.Client, projectIDs []string) error {
	for _, pid := range projectIDs {
		trimmed := strings.TrimSpace(pid)
		if trimmed == "" {
			continue
		}
		isChecked, errCheck := checkCloudAPIIsEnabled(ctx, httpClient, trimmed)
		if errCheck != nil {
			return fmt.Errorf("project %s: %w", trimmed, errCheck)
		}
		if !isChecked {
			return fmt.Errorf("project %s: Cloud AI API not enabled", trimmed)
		}
	}
	return nil
}

func performGeminiCLISetup(ctx context.Context, httpClient *http.Client, storage *geminiAuth.GeminiTokenStorage, requestedProject string) error {
	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	trimmedRequest := strings.TrimSpace(requestedProject)
	explicitProject := trimmedRequest != ""

	loadReqBody := map[string]any{
		"metadata": metadata,
	}
	if explicitProject {
		loadReqBody["cloudaicompanionProject"] = trimmedRequest
	}

	var loadResp map[string]any
	if errLoad := callGeminiCLI(ctx, httpClient, "loadCodeAssist", loadReqBody, &loadResp); errLoad != nil {
		return fmt.Errorf("load code assist: %w", errLoad)
	}

	tierID := "legacy-tier"
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
				if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
					tierID = strings.TrimSpace(id)
					break
				}
			}
		}
	}

	projectID := trimmedRequest
	if projectID == "" {
		if id, okProject := loadResp["cloudaicompanionProject"].(string); okProject {
			projectID = strings.TrimSpace(id)
		}
		if projectID == "" {
			if projectMap, okProject := loadResp["cloudaicompanionProject"].(map[string]any); okProject {
				if id, okID := projectMap["id"].(string); okID {
					projectID = strings.TrimSpace(id)
				}
			}
		}
	}
	if projectID == "" {
		// Auto-discovery: try onboardUser without specifying a project
		// to let Google auto-provision one (matches Gemini CLI headless behavior
		// and Antigravity's FetchProjectID pattern).
		autoOnboardReq := map[string]any{
			"tierId":   tierID,
			"metadata": metadata,
		}

		autoCtx, autoCancel := context.WithTimeout(ctx, 30*time.Second)
		defer autoCancel()
		for attempt := 1; ; attempt++ {
			var onboardResp map[string]any
			if errOnboard := callGeminiCLI(autoCtx, httpClient, "onboardUser", autoOnboardReq, &onboardResp); errOnboard != nil {
				return fmt.Errorf("auto-discovery onboardUser: %w", errOnboard)
			}

			if done, okDone := onboardResp["done"].(bool); okDone && done {
				if resp, okResp := onboardResp["response"].(map[string]any); okResp {
					switch v := resp["cloudaicompanionProject"].(type) {
					case string:
						projectID = strings.TrimSpace(v)
					case map[string]any:
						if id, okID := v["id"].(string); okID {
							projectID = strings.TrimSpace(id)
						}
					}
				}
				break
			}

			log.Debugf("Auto-discovery: onboarding in progress, attempt %d...", attempt)
			select {
			case <-autoCtx.Done():
				return &projectSelectionRequiredError{}
			case <-time.After(2 * time.Second):
			}
		}

		if projectID == "" {
			return &projectSelectionRequiredError{}
		}
		log.Infof("Auto-discovered project ID via onboarding: %s", projectID)
	}

	onboardReqBody := map[string]any{
		"tierId":                  tierID,
		"metadata":                metadata,
		"cloudaicompanionProject": projectID,
	}

	storage.ProjectID = projectID

	for {
		var onboardResp map[string]any
		if errOnboard := callGeminiCLI(ctx, httpClient, "onboardUser", onboardReqBody, &onboardResp); errOnboard != nil {
			return fmt.Errorf("onboard user: %w", errOnboard)
		}

		if done, okDone := onboardResp["done"].(bool); okDone && done {
			responseProjectID := ""
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				switch projectValue := resp["cloudaicompanionProject"].(type) {
				case map[string]any:
					if id, okID := projectValue["id"].(string); okID {
						responseProjectID = strings.TrimSpace(id)
					}
				case string:
					responseProjectID = strings.TrimSpace(projectValue)
				}
			}

			finalProjectID := projectID
			if responseProjectID != "" {
				if explicitProject && !strings.EqualFold(responseProjectID, projectID) {
					log.Infof("Gemini onboarding: requested project %s maps to backend project %s", projectID, responseProjectID)
					log.Infof("Using backend project ID: %s", responseProjectID)
				}
				finalProjectID = responseProjectID
			}

			storage.ProjectID = strings.TrimSpace(finalProjectID)
			if storage.ProjectID == "" {
				storage.ProjectID = strings.TrimSpace(projectID)
			}
			if storage.ProjectID == "" {
				return fmt.Errorf("onboard user completed without project id")
			}
			log.Infof("Onboarding complete. Using Project ID: %s", storage.ProjectID)
			return nil
		}

		log.Println("Onboarding in progress, waiting 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

func callGeminiCLI(ctx context.Context, httpClient *http.Client, endpoint string, body any, result any) error {
	endPointURL := fmt.Sprintf("%s/%s:%s", geminiCLIEndpoint, geminiCLIVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		endPointURL = fmt.Sprintf("%s/%s", geminiCLIEndpoint, endpoint)
	}

	var reader io.Reader
	if body != nil {
		rawBody, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
		reader = bytes.NewReader(rawBody)
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endPointURL, reader)
	if errRequest != nil {
		return fmt.Errorf("create request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if result == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if errDecode := json.NewDecoder(resp.Body).Decode(result); errDecode != nil {
		return fmt.Errorf("decode response body: %w", errDecode)
	}

	return nil
}

func fetchGCPProjects(ctx context.Context, httpClient *http.Client) ([]interfaces.GCPProjectProjects, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if errRequest != nil {
		return nil, fmt.Errorf("could not create project list request: %w", errRequest)
	}

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var projects interfaces.GCPProject
	if errDecode := json.NewDecoder(resp.Body).Decode(&projects); errDecode != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", errDecode)
	}

	return projects.Projects, nil
}

func checkCloudAPIIsEnabled(ctx context.Context, httpClient *http.Client, projectID string) (bool, error) {
	serviceUsageURL := "https://serviceusage.googleapis.com"
	requiredServices := []string{
		"cloudaicompanion.googleapis.com",
	}
	for _, service := range requiredServices {
		checkURL := fmt.Sprintf("%s/v1/projects/%s/services/%s", serviceUsageURL, projectID, service)
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		if resp.StatusCode == http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			if gjson.GetBytes(bodyBytes, "state").String() == "ENABLED" {
				_ = resp.Body.Close()
				continue
			}
		}
		_ = resp.Body.Close()

		enableURL := fmt.Sprintf("%s/v1/projects/%s/services/%s:enable", serviceUsageURL, projectID, service)
		req, errRequest = http.NewRequestWithContext(ctx, http.MethodPost, enableURL, strings.NewReader("{}"))
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", misc.GeminiCLIUserAgent(""))
		resp, errDo = httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		errMessage := string(bodyBytes)
		errMessageResult := gjson.GetBytes(bodyBytes, "error.message")
		if errMessageResult.Exists() {
			errMessage = errMessageResult.String()
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			_ = resp.Body.Close()
			continue
		} else if resp.StatusCode == http.StatusBadRequest {
			_ = resp.Body.Close()
			if strings.Contains(strings.ToLower(errMessage), "already enabled") {
				continue
			}
		}
		_ = resp.Body.Close()
		return false, fmt.Errorf("project activation required: %s", errMessage)
	}
	return true, nil
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if status != "" {
		if strings.HasPrefix(status, "device_code|") {
			parts := strings.SplitN(status, "|", 3)
			if len(parts) == 3 {
				c.JSON(http.StatusOK, gin.H{
					"status":           "device_code",
					"verification_url": parts[1],
					"user_code":        parts[2],
				})
				return
			}
		}
		if strings.HasPrefix(status, "auth_url|") {
			authURL := strings.TrimPrefix(status, "auth_url|")
			c.JSON(http.StatusOK, gin.H{
				"status": "auth_url",
				"url":    authURL,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	if IsOAuthSessionSaving(state, provider) {
		c.JSON(http.StatusOK, gin.H{"status": "wait"})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := strings.TrimSpace(errPoll.Error())
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			case pluginapi.AuthLoginStatusError:
				message := strings.TrimSpace(resp.Message)
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			case pluginapi.AuthLoginStatusSuccess:
				records := pluginLoginPollAuths(host, resp)
				if len(records) == 0 {
					SetOAuthSessionError(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if errSave := h.savePluginLoginRecords(ctx, state, provider, records); errSave != nil {
					if errors.Is(errSave, errOAuthSessionSaving) {
						c.JSON(http.StatusOK, gin.H{"status": "wait"})
						return
					}
					if errors.Is(errSave, errOAuthSessionNotPending) {
						_, _, _, _, saveCompleted, saveSessionExists := GetOAuthSessionDetails(state)
						if saveSessionExists && saveCompleted {
							c.JSON(http.StatusOK, gin.H{"status": "ok"})
							return
						}
						if IsOAuthSessionSaving(state, provider) {
							c.JSON(http.StatusOK, gin.H{"status": "wait"})
							return
						}
						c.JSON(http.StatusOK, gin.H{"status": "error", "error": "OAuth flow is not pending"})
						return
					}
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					SetOAuthSessionError(state, "Failed to save authentication tokens")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
					return
				}
				CompleteOAuthSession(state)
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
				return
			default:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	for _, authData := range authDatas {
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, state, provider string, records []*coreauth.Auth) error {
	if errBegin := beginOAuthSessionSave(state, provider); errBegin != nil {
		return errBegin
	}
	savedPaths := make([]string, 0, len(records))
	for _, record := range records {
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
		if errSave != nil {
			h.rollbackSavedTokenRecords(ctx, savedPaths)
			return errSave
		}
	}
	return nil
}

func (h *Handler) rollbackSavedTokenRecords(ctx context.Context, savedPaths []string) {
	for i := len(savedPaths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(savedPaths[i])
		if path == "" {
			continue
		}
		if errDelete := h.deleteTokenRecord(ctx, path); errDelete != nil {
			log.WithError(errDelete).WithField("path", path).Warn("failed to roll back plugin auth token")
		}
		h.removeAuthsForPath(ctx, path, path)
	}
}

// PopulateAuthContext extracts request info and adds it to the context
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}

const kiroCallbackPort = 9876

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := context.Background()

	// Get the login method from query parameter (default: aws for device code flow)
	method := strings.ToLower(strings.TrimSpace(c.Query("method")))
	if method == "" {
		method = "aws"
	}

	fmt.Println("Initializing Kiro authentication...")

	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())

	switch method {
	case "aws", "builder-id":
		RegisterOAuthSession(state, "kiro")

		// AWS Builder ID uses device code flow (no callback needed)
		go func() {
			ssoClient := kiroauth.NewSSOOIDCClient(h.cfg)

			// Step 1: Register client
			fmt.Println("Registering client...")
			regResp, errRegister := ssoClient.RegisterClient(ctx)
			if errRegister != nil {
				log.Errorf("Failed to register client: %v", errRegister)
				SetOAuthSessionError(state, "Failed to register client")
				return
			}

			// Step 2: Start device authorization
			fmt.Println("Starting device authorization...")
			authResp, errAuth := ssoClient.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
			if errAuth != nil {
				log.Errorf("Failed to start device auth: %v", errAuth)
				SetOAuthSessionError(state, "Failed to start device authorization")
				return
			}

			// Store the verification URL for the frontend to display.
			// Using "|" as separator because URLs contain ":".
			SetOAuthSessionError(state, "device_code|"+authResp.VerificationURIComplete+"|"+authResp.UserCode)

			// Step 3: Poll for token
			fmt.Println("Waiting for authorization...")
			interval := 5 * time.Second
			if authResp.Interval > 0 {
				interval = time.Duration(authResp.Interval) * time.Second
			}
			deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					SetOAuthSessionError(state, "Authorization cancelled")
					return
				case <-time.After(interval):
					tokenResp, errToken := ssoClient.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
					if errToken != nil {
						errStr := errToken.Error()
						if strings.Contains(errStr, "authorization_pending") {
							continue
						}
						if strings.Contains(errStr, "slow_down") {
							interval += 5 * time.Second
							continue
						}
						log.Errorf("Token creation failed: %v", errToken)
						SetOAuthSessionError(state, "Token creation failed")
						return
					}

					// Success! Save the token
					expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

					idPart := kiroauth.SanitizeEmailForFilename(email)
					if idPart == "" {
						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
					}

					now := time.Now()
					fileName := fmt.Sprintf("kiro-aws-%s.json", idPart)

					record := &coreauth.Auth{
						ID:       fileName,
						Provider: "kiro",
						FileName: fileName,
						Metadata: map[string]any{
							"type":          "kiro",
							"access_token":  tokenResp.AccessToken,
							"refresh_token": tokenResp.RefreshToken,
							"expires_at":    expiresAt.Format(time.RFC3339),
							"auth_method":   "builder-id",
							"provider":      "AWS",
							"client_id":     regResp.ClientID,
							"client_secret": regResp.ClientSecret,
							"email":         email,
							"last_refresh":  now.Format(time.RFC3339),
						},
					}

					savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "kiro", record)
					if errors.Is(errSave, errOAuthSessionNotPending) {
						return
					}
					if errSave != nil {
						log.Errorf("Failed to save authentication tokens: %v", errSave)
						SetOAuthSessionError(state, "Failed to save authentication tokens")
						return
					}

					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
					if email != "" {
						fmt.Printf("Authenticated as: %s\n", email)
					}
					CompleteOAuthSession(state)
					return
				}
			}

			SetOAuthSessionError(state, "Authorization timed out")
		}()

		// Return immediately with the state for polling
		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "device_code"})

	case "google", "github":
		RegisterOAuthSession(state, "kiro")

		// Social auth uses protocol handler - for WEB UI we use a callback forwarder
		provider := "Google"
		if method == "github" {
			provider = "Github"
		}

		isWebUI := isWebUIRequest(c)
		var forwarder *callbackForwarder
		if isWebUI {
			targetURL, errTarget := h.managementCallbackURL("/kiro/callback")
			if errTarget != nil {
				log.WithError(errTarget).Error("failed to compute kiro callback target")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
				return
			}
			var errStart error
			if forwarder, errStart = startCallbackForwarder(kiroCallbackPort, "kiro", targetURL); errStart != nil {
				log.WithError(errStart).Error("failed to start kiro callback forwarder")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
				return
			}
		}

		go func() {
			if isWebUI {
				defer stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
			}

			socialClient := kiroauth.NewSocialAuthClient(h.cfg)

			// Generate PKCE codes
			codeVerifier, codeChallenge, errPKCE := generateKiroPKCE()
			if errPKCE != nil {
				log.Errorf("Failed to generate PKCE: %v", errPKCE)
				SetOAuthSessionError(state, "Failed to generate PKCE")
				return
			}

			// Build login URL
			authURL := fmt.Sprintf("%s/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&prompt=select_account",
				"https://prod.us-east-1.auth.desktop.kiro.dev",
				provider,
				url.QueryEscape(kiroauth.KiroRedirectURI),
				codeChallenge,
				state,
			)

			// Store auth URL for frontend.
			// Using "|" as separator because URLs contain ":".
			SetOAuthSessionError(state, "auth_url|"+authURL)

			// Wait for callback file
			waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-kiro-%s.oauth", state))
			deadline := time.Now().Add(5 * time.Minute)

			for {
				if time.Now().After(deadline) {
					log.Error("oauth flow timed out")
					SetOAuthSessionError(state, "OAuth flow timed out")
					return
				}
				if data, errRead := os.ReadFile(waitFile); errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(waitFile)
					if errStr := m["error"]; errStr != "" {
						log.Errorf("Authentication failed: %s", errStr)
						SetOAuthSessionError(state, "Authentication failed")
						return
					}
					if m["state"] != state {
						log.Errorf("State mismatch")
						SetOAuthSessionError(state, "State mismatch")
						return
					}
					code := m["code"]
					if code == "" {
						log.Error("No authorization code received")
						SetOAuthSessionError(state, "No authorization code received")
						return
					}

					// Exchange code for tokens
					tokenReq := &kiroauth.CreateTokenRequest{
						Code:         code,
						CodeVerifier: codeVerifier,
						RedirectURI:  kiroauth.KiroRedirectURI,
					}

					tokenResp, errToken := socialClient.CreateToken(ctx, tokenReq)
					if errToken != nil {
						log.Errorf("Failed to exchange code for tokens: %v", errToken)
						SetOAuthSessionError(state, "Failed to exchange code for tokens")
						return
					}

					// Save the token
					expiresIn := tokenResp.ExpiresIn
					if expiresIn <= 0 {
						expiresIn = 3600
					}
					expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

					idPart := kiroauth.SanitizeEmailForFilename(email)
					if idPart == "" {
						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
					}

					now := time.Now()
					fileName := fmt.Sprintf("kiro-%s-%s.json", strings.ToLower(provider), idPart)

					record := &coreauth.Auth{
						ID:       fileName,
						Provider: "kiro",
						FileName: fileName,
						Metadata: map[string]any{
							"type":          "kiro",
							"access_token":  tokenResp.AccessToken,
							"refresh_token": tokenResp.RefreshToken,
							"profile_arn":   tokenResp.ProfileArn,
							"expires_at":    expiresAt.Format(time.RFC3339),
							"auth_method":   "social",
							"provider":      provider,
							"email":         email,
							"last_refresh":  now.Format(time.RFC3339),
						},
					}

					savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "kiro", record)
					if errors.Is(errSave, errOAuthSessionNotPending) {
						return
					}
					if errSave != nil {
						log.Errorf("Failed to save authentication tokens: %v", errSave)
						SetOAuthSessionError(state, "Failed to save authentication tokens")
						return
					}

					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
					if email != "" {
						fmt.Printf("Authenticated as: %s\n", email)
					}
					CompleteOAuthSession(state)
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()

		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "social"})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid method, use 'aws', 'google', or 'github'"})
	}
}

// generateKiroPKCE generates PKCE code verifier and challenge for Kiro OAuth.
func generateKiroPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, errRead := io.ReadFull(rand.Reader, b); errRead != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", errRead)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge, nil
}

func (h *Handler) RequestKiloToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing Kilo authentication...")

	state := fmt.Sprintf("kil-%d", time.Now().UnixNano())
	kilocodeAuth := kilo.NewKiloAuth()

	resp, err := kilocodeAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Errorf("Failed to initiate device flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
		return
	}

	RegisterOAuthSession(state, "kilo")

	go func() {
		fmt.Printf("Please visit %s and enter code: %s\n", resp.VerificationURL, resp.Code)

		status, err := kilocodeAuth.PollForToken(ctx, resp.Code)
		if err != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", err)
			return
		}

		profile, err := kilocodeAuth.GetProfile(ctx, status.Token)
		if err != nil {
			log.Warnf("Failed to fetch profile: %v", err)
			profile = &kilo.Profile{Email: status.UserEmail}
		}

		var orgID string
		if len(profile.Orgs) > 0 {
			orgID = profile.Orgs[0].ID
		}

		defaults, err := kilocodeAuth.GetDefaults(ctx, status.Token, orgID)
		if err != nil {
			defaults = &kilo.Defaults{}
		}

		ts := &kilo.KiloTokenStorage{
			Token:          status.Token,
			OrganizationID: orgID,
			Model:          defaults.Model,
			Email:          status.UserEmail,
			Type:           "kilo",
		}

		fileName := kilo.CredentialFileName(status.UserEmail)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kilo",
			FileName: fileName,
			Storage:  ts,
			Metadata: map[string]any{
				"email":           status.UserEmail,
				"organization_id": orgID,
				"model":           defaults.Model,
			},
		}

		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "kilo", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("kilo")
	}()

	c.JSON(200, gin.H{
		"status":           "ok",
		"url":              resp.VerificationURL,
		"state":            state,
		"user_code":        resp.Code,
		"verification_uri": resp.VerificationURL,
	})
}

// RequestCursorToken initiates the Cursor PKCE authentication flow.
// Supports multiple accounts via ?label=xxx query parameter.
// The user opens the returned URL in a browser, logs in, and the server polls
// until the authentication completes.
func (h *Handler) RequestCursorToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	label := strings.TrimSpace(c.Query("label"))
	log.Infof("Initializing Cursor authentication (label=%q)...", label)

	authParams, err := cursorauth.GenerateAuthParams()
	if err != nil {
		log.Errorf("Failed to generate Cursor auth params: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate auth params"})
		return
	}

	state := fmt.Sprintf("cur-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "cursor")
	proxyURL := ""
	if h != nil && h.cfg != nil {
		proxyURL = h.cfg.ProxyURL
	}

	go func() {
		log.Info("Waiting for Cursor authentication...")
		log.Infof("Open this URL in your browser: %s", authParams.LoginURL)

		httpClient, errClient := cursorauth.NewHTTPClient(proxyURL, 10*time.Second)
		if errClient != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errClient.Error())
			log.Errorf("Cursor authentication failed: %v", errClient)
			return
		}
		tokens, errPoll := cursorauth.PollForAuthWithClient(ctx, authParams.UUID, authParams.Verifier, httpClient)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			log.Errorf("Cursor authentication failed: %v", errPoll)
			return
		}

		// Build metadata
		metadata := map[string]any{
			"type":          "cursor",
			"access_token":  tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
			"timestamp":     time.Now().UnixMilli(),
		}

		// Extract expiry and account identity from JWT
		expiry := cursorauth.GetTokenExpiry(tokens.AccessToken)
		if !expiry.IsZero() {
			metadata["expires_at"] = expiry.Format(time.RFC3339)
		}

		// Auto-identify account from JWT sub claim for multi-account support
		sub := cursorauth.ParseJWTSub(tokens.AccessToken)
		subHash := cursorauth.SubToShortHash(sub)
		if sub != "" {
			metadata["sub"] = sub
		}

		fileName := cursorauth.CredentialFileName(label, subHash)
		displayLabel := cursorauth.DisplayLabel(label, subHash)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "cursor",
			FileName: fileName,
			Label:    displayLabel,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveOAuthTokenRecord(ctx, state, "cursor", record)
		if errors.Is(errSave, errOAuthSessionNotPending) {
			return
		}
		if errSave != nil {
			log.Errorf("Failed to save Cursor tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save tokens")
			return
		}

		log.Infof("Cursor authentication successful! Token saved to %s", savedPath)
		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("cursor")
	}()

	c.JSON(200, gin.H{
		"status": "ok",
		"url":    authParams.LoginURL,
		"state":  state,
	})
}
