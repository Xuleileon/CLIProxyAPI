package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// OpenCodeGoExecutor dispatches each subscription model to its required wire protocol.
type OpenCodeGoExecutor struct {
	cfg  *config.Config
	chat *OpenAICompatExecutor
}

func NewOpenCodeGoExecutor(cfg *config.Config) *OpenCodeGoExecutor {
	return &OpenCodeGoExecutor{cfg: cfg, chat: NewOpenAICompatExecutor("opencode-go", cfg)}
}

func (e *OpenCodeGoExecutor) Identifier() string { return "opencode-go" }

// RequestToFormat reports the built-in upstream protocol to request interceptors.
func (e *OpenCodeGoExecutor) RequestToFormat(req cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return openCodeGoTranslatorFormat(e.protocolFor(nil, req.Model))
}

func (e *OpenCodeGoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	protocol := e.protocolFor(auth, req.Model)
	if protocol == config.OpenCodeGoProtocolOpenAI {
		return e.chat.Execute(ctx, auth, req, opts)
	}
	return e.executeNative(ctx, auth, req, opts, protocol)
}

func (e *OpenCodeGoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	protocol := e.protocolFor(auth, req.Model)
	if protocol == config.OpenCodeGoProtocolOpenAI {
		return e.chat.ExecuteStream(ctx, auth, req, opts)
	}
	return e.executeNativeStream(ctx, auth, req, opts, protocol)
}

func (e *OpenCodeGoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.chat.CountTokens(ctx, auth, req, opts)
}

func (e *OpenCodeGoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debug("opencode-go executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *OpenCodeGoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode-go executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	e.applyHeaders(httpReq, auth, strings.HasSuffix(strings.TrimRight(httpReq.URL.Path, "/"), "/messages"))
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

func (e *OpenCodeGoExecutor) executeNative(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, protocol string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseURL, _ := openCodeGoCredentials(auth)
	if baseURL == "" {
		baseURL = config.DefaultOpenCodeGoBaseURL
	}
	to := openCodeGoTranslatorFormat(protocol)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(opts.SourceFormat, to, baseModel, original, false)
	translated := sdktranslator.TranslateRequest(opts.SourceFormat, to, baseModel, req.Payload, false)
	translated, err = e.preparePayload(translated, originalTranslated, req, opts, protocol, false)
	if err != nil {
		return resp, err
	}

	endpoint := openCodeGoEndpoint(protocol)
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+endpoint, bytes.NewReader(translated))
	if errRequest != nil {
		return resp, errRequest
	}
	e.applyHeaders(httpReq, auth, protocol == config.OpenCodeGoProtocolClaude)
	e.recordRequest(ctx, auth, httpReq, translated)
	httpResp, errHTTP := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if errHTTP != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errHTTP)
		return resp, errHTTP
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode-go executor: close response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(httpResp.Body)
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if errRead != nil {
		return resp, errRead
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *OpenCodeGoExecutor) executeNativeStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, protocol string) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseURL, _ := openCodeGoCredentials(auth)
	if baseURL == "" {
		baseURL = config.DefaultOpenCodeGoBaseURL
	}
	to := openCodeGoTranslatorFormat(protocol)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(opts.SourceFormat, to, baseModel, original, true)
	translated := sdktranslator.TranslateRequest(opts.SourceFormat, to, baseModel, req.Payload, true)
	translated, errPrepare := e.preparePayload(translated, originalTranslated, req, opts, protocol, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+openCodeGoEndpoint(protocol), bytes.NewReader(translated))
	if errRequest != nil {
		return nil, errRequest
	}
	e.applyHeaders(httpReq, auth, protocol == config.OpenCodeGoProtocolClaude)
	httpReq.Header.Set("Accept", "text/event-stream")
	e.recordRequest(ctx, auth, httpReq, translated)
	httpResp, errHTTP := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if errHTTP != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errHTTP)
		return nil, errHTTP
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go e.translateStream(ctx, httpResp.Body, out, to, responseFormat, req, opts, translated)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenCodeGoExecutor) translateStream(ctx context.Context, body io.ReadCloser, out chan<- cliproxyexecutor.StreamChunk, from, to sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, translated []byte) {
	defer close(out)
	defer func() {
		if errClose := body.Close(); errClose != nil {
			log.Errorf("opencode-go executor: close stream body error: %v", errClose)
		}
	}()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	var param any
	var nativeEvent bytes.Buffer
	flushNative := func() bool {
		if nativeEvent.Len() == 0 {
			return true
		}
		payload := bytes.Clone(nativeEvent.Bytes())
		nativeEvent.Reset()
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		if from == to {
			nativeEvent.Write(line)
			nativeEvent.WriteByte('\n')
			if len(bytes.TrimSpace(line)) == 0 && !flushNative() {
				return
			}
			continue
		}
		for _, chunk := range sdktranslator.TranslateStream(ctx, from, to, req.Model, opts.OriginalRequest, translated, line, &param) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
	}
	if from == to && !flushNative() {
		return
	}
	if errScan := scanner.Err(); errScan != nil {
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
		case <-ctx.Done():
		}
	}
}

func (e *OpenCodeGoExecutor) preparePayload(payload, original []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, protocol string, stream bool) ([]byte, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	to := openCodeGoTranslatorFormat(protocol)
	var err error
	if protocol != config.OpenCodeGoProtocolClaude {
		payload, err = thinking.ApplyThinking(payload, req.Model, opts.SourceFormat.String(), to.String(), e.Identifier())
		if err != nil {
			return nil, err
		}
	}
	payload = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), opts.SourceFormat.String(), "", payload, original, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	payload, _ = sjson.SetBytes(payload, "model", baseModel)
	payload, _ = sjson.SetBytes(payload, "stream", stream)
	if protocol == config.OpenCodeGoProtocolClaude {
		payload = sanitizeOpenCodeGoAnthropicPayload(payload)
		payload = ensureModelMaxTokens(payload, baseModel)
	}
	return payload, nil
}

func (e *OpenCodeGoExecutor) applyHeaders(req *http.Request, auth *cliproxyauth.Auth, anthropic bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-opencode-go")
	_, apiKey := openCodeGoCredentials(auth)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		if anthropic {
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
}

func (e *OpenCodeGoExecutor) recordRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request, body []byte) {
	logEntry := helps.UpstreamRequestLog{URL: req.URL.String(), Method: req.Method, Headers: req.Header.Clone(), Body: body, Provider: e.Identifier()}
	if auth != nil {
		logEntry.AuthID = auth.ID
		logEntry.AuthLabel = auth.Label
		logEntry.AuthType, logEntry.AuthValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, logEntry)
}

func (e *OpenCodeGoExecutor) protocolFor(auth *cliproxyauth.Auth, model string) string {
	model = strings.TrimSpace(model)
	entries := make([]*config.OpenCodeGoKey, 0, 1)
	if entry := e.resolveConfig(auth); entry != nil {
		entries = append(entries, entry)
	} else if auth == nil && e != nil && e.cfg != nil {
		for i := range e.cfg.OpenCodeGoKey {
			entries = append(entries, &e.cfg.OpenCodeGoKey[i])
		}
	}
	for _, entry := range entries {
		for i := range entry.Models {
			configured := entry.Models[i]
			if strings.EqualFold(model, strings.TrimSpace(configured.Name)) || strings.EqualFold(model, strings.TrimSpace(configured.Alias)) {
				if protocol := config.NormalizeOpenCodeGoProtocol(configured.Protocol); protocol != "" {
					return protocol
				}
			}
		}
	}
	return registry.OpenCodeGoProtocolForModel(model)
}

func (e *OpenCodeGoExecutor) resolveConfig(auth *cliproxyauth.Auth) *config.OpenCodeGoKey {
	if e == nil || e.cfg == nil || auth == nil {
		return nil
	}
	_, apiKey := openCodeGoCredentials(auth)
	baseURL := ""
	if auth.Attributes != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/")
	}
	for i := range e.cfg.OpenCodeGoKey {
		entry := &e.cfg.OpenCodeGoKey[i]
		if apiKey != "" && !strings.EqualFold(apiKey, strings.TrimSpace(entry.APIKey)) {
			continue
		}
		if baseURL != "" && !strings.EqualFold(baseURL, strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")) {
			continue
		}
		return entry
	}
	return nil
}

func openCodeGoCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth != nil && auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return baseURL, apiKey
}

func openCodeGoTranslatorFormat(protocol string) sdktranslator.Format {
	switch protocol {
	case config.OpenCodeGoProtocolClaude:
		return sdktranslator.FormatClaude
	case config.OpenCodeGoProtocolResponses:
		return sdktranslator.FormatOpenAIResponse
	default:
		return sdktranslator.FormatOpenAI
	}
}

func openCodeGoEndpoint(protocol string) string {
	switch protocol {
	case config.OpenCodeGoProtocolClaude:
		return "/messages"
	case config.OpenCodeGoProtocolResponses:
		return "/responses"
	default:
		return "/chat/completions"
	}
}

func sanitizeOpenCodeGoAnthropicPayload(payload []byte) []byte {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return payload
	}
	for _, key := range []string{"thinking", "reasoning", "reasoning_effort", "effort", "level", "depth", "output_config"} {
		delete(root, key)
	}
	root = stripOpenCodeGoAnthropicExtensions(root).(map[string]any)
	if system, ok := root["system"].([]any); ok {
		parts := make([]string, 0, len(system))
		for _, item := range system {
			if block, okBlock := item.(map[string]any); okBlock {
				if text, okText := block["text"].(string); okText && text != "" {
					parts = append(parts, text)
				}
			}
		}
		root["system"] = strings.Join(parts, "\n")
	}
	clean, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return payload
	}
	return clean
}

func stripOpenCodeGoAnthropicExtensions(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "cache_control" || key == "signature" {
				continue
			}
			out[key] = stripOpenCodeGoAnthropicExtensions(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			if block, ok := child.(map[string]any); ok {
				typeName, _ := block["type"].(string)
				if typeName == "thinking" || typeName == "redacted_thinking" {
					continue
				}
			}
			out = append(out, stripOpenCodeGoAnthropicExtensions(child))
		}
		return out
	default:
		return value
	}
}
