package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	cursorAPIURL     = "https://api2.cursor.sh"
	cursorRunPath    = "/agent.v1.AgentService/Run"
	cursorModelsPath = "/agent.v1.AgentService/GetUsableModels"
	// Match Cursor IDE client identity. "cli" is a separate product surface that
	// often only has the fast-request pool (no IDE-style slow fallback).
	cursorClientVersion     = "3.13.25"
	cursorClientCommit      = "31e8d61c448c7472e371505838a0fe34083dad50"
	cursorClientType        = "ide"
	cursorAuthType          = "cursor"
	cursorHeartbeatInterval = 5 * time.Second
	cursorSessionTTL        = 5 * time.Minute
	cursorCheckpointTTL     = 30 * time.Minute
)

// CursorExecutor handles requests to the Cursor API via Connect+Protobuf protocol.
type CursorExecutor struct {
	cfg         *config.Config
	h2Pool      *cursorproto.H2StreamPool
	mu          sync.Mutex
	sessions    map[string]*cursorSession
	checkpoints map[string]*savedCheckpoint // keyed by conversationId
}

// savedCheckpoint stores the server's conversation_checkpoint_update for reuse.
type savedCheckpoint struct {
	data      []byte            // raw ConversationStateStructure protobuf bytes
	blobStore map[string][]byte // blobs referenced by the checkpoint
	authID    string            // auth that produced this checkpoint (checkpoint is auth-specific)
	updatedAt time.Time
}

type cursorSession struct {
	stream         *cursorproto.H2Stream
	pending        []pendingMcpExec
	toolResultCh   chan []toolResultInfo
	resumeOutCh    chan cliproxyexecutor.StreamChunk
	switchOutput   func(chan cliproxyexecutor.StreamChunk)
	streamDone     <-chan struct{}
	resuming       bool
	cancel         context.CancelFunc // cancels the session-scoped heartbeat (NOT tied to HTTP request)
	createdAt      time.Time
	authID         string // auth file ID that created this session (for multi-account isolation)
	conversationID string // Cursor conversation inherited by fresh tool continuations
}

type pendingMcpExec struct {
	ExecMsgId  uint32
	ExecId     string
	ToolCallId string
	ToolName   string
	Args       string // JSON-encoded args
	Kind       cursorExecKind
	Path       string
	Command    string
	WorkDir    string
	URL        string
	Pattern    string
	OutputMode string
	FileText   string
}

type cursorExecKind int

const (
	cursorExecMCP cursorExecKind = iota
	cursorExecShell
	cursorExecShellStream
	cursorExecRead
	cursorExecWrite
	cursorExecDelete
	cursorExecLs
	cursorExecGrep
	cursorExecFetch
)

// NewCursorExecutor constructs a new executor instance.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	e := &CursorExecutor{
		cfg:         cfg,
		h2Pool:      cursorproto.NewH2StreamPool(),
		sessions:    make(map[string]*cursorSession),
		checkpoints: make(map[string]*savedCheckpoint),
	}
	go e.cleanupLoop()
	return e
}

// Identifier implements ProviderExecutor.
func (e *CursorExecutor) Identifier() string { return cursorAuthType }

// CloseExecutionSession implements ExecutionSessionCloser.
func (e *CursorExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	var sessions []*cursorSession
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		for k, s := range e.sessions {
			sessions = append(sessions, s)
			delete(e.sessions, k)
		}
	} else if s, ok := e.sessions[sessionID]; ok {
		sessions = append(sessions, s)
		delete(e.sessions, sessionID)
	}
	e.mu.Unlock()
	for _, session := range sessions {
		closeCursorSession(session)
	}
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID && e.h2Pool != nil {
		e.h2Pool.CloseIdleConnections()
	}
}

func (e *CursorExecutor) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		e.mu.Lock()
		var expired []*cursorSession
		for k, s := range e.sessions {
			if time.Since(s.createdAt) > cursorSessionTTL {
				expired = append(expired, s)
				delete(e.sessions, k)
			}
		}
		for k, cp := range e.checkpoints {
			if time.Since(cp.updatedAt) > cursorCheckpointTTL {
				delete(e.checkpoints, k)
			}
		}
		e.mu.Unlock()
		for _, session := range expired {
			closeCursorSession(session)
		}
	}
}

func closeCursorSession(session *cursorSession) {
	if session == nil {
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	if session.stream != nil {
		session.stream.Close()
	}
}

func cloneCursorBlobStore(src map[string][]byte) map[string][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]byte, len(src))
	for key, value := range src {
		dst[key] = append([]byte(nil), value...)
	}
	return dst
}

func cloneSavedCursorCheckpoint(src *savedCheckpoint) (*savedCheckpoint, bool) {
	if src == nil {
		return nil, false
	}
	return &savedCheckpoint{
		data:      append([]byte(nil), src.data...),
		blobStore: cloneCursorBlobStore(src.blobStore),
		authID:    src.authID,
		updatedAt: src.updatedAt,
	}, true
}

func sessionMatchesToolResults(session *cursorSession, results []toolResultInfo) bool {
	if session == nil {
		return false
	}
	for _, pending := range session.pending {
		for _, result := range results {
			if pending.ToolCallId != "" && pending.ToolCallId == result.ToolCallId {
				return true
			}
		}
	}
	return false
}

func (e *CursorExecutor) hasPendingSessionForStreamLocked(stream *cursorproto.H2Stream) bool {
	for _, session := range e.sessions {
		if session != nil && session.stream == stream && len(session.pending) > 0 {
			return true
		}
	}
	return false
}

func (e *CursorExecutor) closeStreamWhenDownstreamEnds(ctx context.Context, stream *cursorproto.H2Stream, cancel context.CancelFunc) {
	go func() {
		select {
		case <-ctx.Done():
		case <-stream.Done():
			return
		}

		e.mu.Lock()
		waitingForTool := e.hasPendingSessionForStreamLocked(stream)
		e.mu.Unlock()
		if waitingForTool {
			return
		}

		log.Debugf("cursor: downstream ended with no pending tool call; closing H2 stream %s", stream.ID())
		cancel()
		stream.Close()
	}()
}

// findSessionByConversationLocked searches for a session matching the given
// conversationId regardless of authID. Used to find and clean up stale sessions
// from a previous auth after quota failover. Caller must hold e.mu.
func (e *CursorExecutor) findSessionByConversationLocked(convId string) string {
	suffix := ":" + convId
	for k := range e.sessions {
		if strings.HasSuffix(k, suffix) {
			return k
		}
	}
	return ""
}

// findSessionByToolResultsLocked finds the active H2 session whose pending tool
// call produced one of the returned results. Results are searched newest first
// because stateless clients resend the full tool history on every request.
// Caller must hold e.mu.
func (e *CursorExecutor) findSessionByToolResultsLocked(authID string, results []toolResultInfo) (string, *cursorSession) {
	for resultIndex := len(results) - 1; resultIndex >= 0; resultIndex-- {
		toolCallID := results[resultIndex].ToolCallId
		if toolCallID == "" {
			continue
		}
		for sessionKey, session := range e.sessions {
			if session == nil || session.authID != authID || session.resuming {
				continue
			}
			for _, pending := range session.pending {
				if pending.ToolCallId == toolCallID {
					return sessionKey, session
				}
			}
		}
	}
	return "", nil
}

// cursorStatusErr implements the StatusError and RetryAfter interfaces so the
// conductor can classify Cursor errors (e.g. 429 → quota cooldown).
type cursorStatusErr struct {
	code int
	msg  string
}

func (e cursorStatusErr) Error() string              { return e.msg }
func (e cursorStatusErr) StatusCode() int            { return e.code }
func (e cursorStatusErr) RetryAfter() *time.Duration { return nil } // no retry-after info from Cursor; conductor uses exponential backoff

// classifyCursorError maps Cursor Connect/H2 errors to HTTP status codes.
// Layer 1: precise match on ConnectError.Code (gRPC standard codes).
// Layer 2: fuzzy string match for H2 frame errors and unknown formats.
// Unclassified errors pass through unchanged.
func classifyCursorError(err error) error {
	if err == nil {
		return nil
	}

	// Layer 1: structured ConnectError from ParseConnectEndStream
	var ce *cursorproto.ConnectError
	if errors.As(err, &ce) {
		log.Infof("cursor: Connect error code=%q message=%q", ce.Code, ce.Message)
		switch ce.Code {
		case "resource_exhausted":
			return cursorStatusErr{code: 429, msg: err.Error()}
		case "unauthenticated":
			return cursorStatusErr{code: 401, msg: err.Error()}
		case "permission_denied":
			return cursorStatusErr{code: 403, msg: err.Error()}
		case "unavailable":
			return cursorStatusErr{code: 503, msg: err.Error()}
		case "internal":
			return cursorStatusErr{code: 500, msg: err.Error()}
		default:
			// Unknown Connect code — log for observation, treat as 502
			return cursorStatusErr{code: 502, msg: err.Error()}
		}
	}

	// Layer 2: fuzzy match for H2 errors and unstructured messages
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "too many"):
		return cursorStatusErr{code: 429, msg: err.Error()}
	case strings.Contains(msg, "rst_stream") || strings.Contains(msg, "goaway"):
		return cursorStatusErr{code: 502, msg: err.Error()}
	}

	return err
}

// PrepareRequest implements ProviderExecutor (for HttpRequest support).
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	token := cursorAccessToken(auth)
	if token == "" {
		return fmt.Errorf("cursor: access token not found")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor: request is nil")
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	httpClient, err := cursorauth.NewHTTPClient(cursorProxyURL(e.cfg, auth), 0)
	if err != nil {
		return nil, err
	}
	return httpClient.Do(req)
}

// CountTokens estimates token count locally using tiktoken.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	defer func() {
		if err != nil {
			log.Warnf("cursor CountTokens error: %v", err)
		} else {
			log.Debugf("cursor CountTokens: model=%s result=%s", req.Model, string(resp.Payload))
		}
	}()
	model := gjson.GetBytes(req.Payload, "model").String()
	if model == "" {
		model = req.Model
	}

	enc, err := getTokenizer(model)
	if err != nil {
		// Fallback: return zero tokens rather than error (avoids 502)
		return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(0)}, nil
	}

	// Detect format: Claude (/v1/messages) vs OpenAI (/v1/chat/completions)
	var count int64
	if gjson.GetBytes(req.Payload, "system").Exists() || opts.SourceFormat.String() == "claude" {
		count, _ = countClaudeChatTokens(enc, req.Payload)
	} else {
		count, _ = countOpenAIChatTokens(enc, req.Payload)
	}

	return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(count)}, nil
}

// Refresh attempts to refresh the Cursor access token.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	refreshToken := cursorRefreshToken(auth)
	if refreshToken == "" {
		return nil, fmt.Errorf("cursor: no refresh token available")
	}

	httpClient, err := cursorauth.NewHTTPClient(cursorProxyURL(e.cfg, auth), 10*time.Second)
	if err != nil {
		return nil, err
	}
	tokens, err := cursorauth.RefreshTokenWithClient(ctx, refreshToken, httpClient)
	if err != nil {
		return nil, err
	}

	expiresAt := cursorauth.GetTokenExpiry(tokens.AccessToken)

	newAuth := auth.Clone()
	newAuth.Metadata["access_token"] = tokens.AccessToken
	newAuth.Metadata["refresh_token"] = tokens.RefreshToken
	newAuth.Metadata["expires_at"] = expiresAt.Format(time.RFC3339)
	return newAuth, nil
}

// Execute handles non-streaming requests.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	log.Debugf("cursor Execute: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor Execute PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor Execute error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("cursor: access token not found")
	}

	// Translate input to OpenAI format if needed (e.g. Claude /v1/messages format)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	if from.String() != "" && from.String() != "openai" {
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), false)
	}

	parsed := parseOpenAIRequest(payload)
	// Non-stream OpenAI chat is request-scoped: Cursor ignores structured turns and
	// reusing a conversation_id without server checkpoint causes "missing blob" errors
	// on multi-turn payloads. Flatten history into UserText when present.
	conversationId := deriveConversationId(apiKeyFromContext(ctx), "", parsed.SystemPrompt)
	if len(parsed.Turns) > 0 || len(parsed.ToolResults) > 0 {
		flattenConversationIntoUserText(parsed)
	}
	params := buildRunRequestParams(parsed, conversationId, req.Model)

	requestBytes := cursorproto.EncodeRunRequest(params)
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)
	log.Debugf("cursor: encoded Run request bytes=%d userTextBytes=%d checkpoint=%t conv=%s", len(requestBytes), len(params.UserText), len(params.RawCheckpoint) > 0, conversationId)

	stream, err := e.openCursorH2Stream(ctx, auth, accessToken)
	if err != nil {
		return resp, err
	}
	defer stream.Close()

	// Send the request frame
	if err := stream.Write(framedRequest); err != nil {
		return resp, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	// Start heartbeat
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	go cursorH2Heartbeat(sessionCtx, stream)

	// Collect content and reasoning separately (Codex-compatible split).
	var contentText, reasoningText strings.Builder
	usage := &cursorTokenUsage{}
	usage.setInputEstimate(len(payload))
	if streamErr := processH2SessionFrames(sessionCtx, stream, params.BlobStore, nil,
		func(text string, isThinking bool) {
			if isThinking {
				reasoningText.WriteString(text)
			} else {
				contentText.WriteString(text)
			}
		},
		nil,
		nil,
		usage,
		nil, // onCheckpoint - non-streaming doesn't persist
	); streamErr != nil && contentText.Len() == 0 && reasoningText.Len() == 0 {
		return resp, classifyCursorError(fmt.Errorf("cursor: stream error: %w", streamErr))
	}

	id := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()
	inTok, outTok := usage.get()
	openaiResp := buildCursorOpenAIChatCompletion(id, created, parsed.Model, contentText.String(), reasoningText.String(), inTok, outTok)

	// Translate response back to source format if needed
	result := openaiResp
	if from.String() != "" && from.String() != "openai" {
		var param any
		result = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), payload, result, &param)
		result = normalizeCursorResponsesUsage(from, result)
	}
	resp.Payload = result
	return resp, nil
}

// ExecuteStream handles streaming requests.
// It supports MCP tool call sessions: when Cursor returns an MCP tool call,
// the H2 stream stays alive while the client executes it. The next HTTP request
// injects the result into that same Cursor Run and attaches a fresh output channel.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("cursor ExecuteStream: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor ExecuteStream PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor ExecuteStream error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("cursor: access token not found")
	}

	// Resolve the downstream conversation identity before translation strips metadata.
	// Headers are authoritative for Claude Code; body and derived identities are fallbacks.
	rawSourceSessionID := cursorSourceSessionID(req, opts)

	// Translate input to OpenAI format if needed
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	if from.String() != "" && from.String() != "openai" {
		log.Debugf("cursor: translating request from %s to openai", from)
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), true)
		log.Debugf("cursor: translated payload len=%d", len(payload))
	}

	parsed := parseOpenAIRequest(payload)
	applyOriginalToolResultErrors(parsed, originalPayload)
	adaptCursorTeammateReply(parsed)
	log.Debugf("cursor: parsed request: model=%s userText=%d chars, turns=%d, tools=%d, toolResults=%d",
		parsed.Model, len(parsed.UserText), len(parsed.Turns), len(parsed.Tools), len(parsed.ToolResults))

	sourceSessionID := scopeCursorSourceSessionID(rawSourceSessionID, parsed)
	conversationId := deriveConversationId(apiKeyFromContext(ctx), sourceSessionID, parsed.SystemPrompt)
	authID := auth.ID // e.g. "cursor.json" or "cursor-account2.json"
	log.Debugf("cursor: conversationId=%s authID=%s stableSource=%t", conversationId, authID, sourceSessionID != "")

	// Session key includes authID (H2 stream is auth-specific, not transferable).
	// Checkpoint key uses conversationId only — allows detecting auth migration.
	sessionKey := authID + ":" + conversationId
	checkpointKey := conversationId
	needsTranslate := from.String() != "" && from.String() != "openai"

	var continuationPending []pendingMcpExec
	continuationMatched := false

	// Match tool results to the exact pending Cursor tool call. Reusing the active
	// H2 Run preserves Cursor's native tool loop and avoids one billed Run per tool.
	// A saved checkpoint remains the fallback if the live stream has already ended.
	if len(parsed.ToolResults) > 0 {
		e.mu.Lock()
		session, hasSession := e.sessions[sessionKey]
		matchedSessionKey := sessionKey
		if hasSession && !session.resuming && sessionMatchesToolResults(session, parsed.ToolResults) {
			session.resuming = true
		} else {
			session = nil
			hasSession = false
		}
		if !hasSession {
			if matchedKey, matchedSession := e.findSessionByToolResultsLocked(authID, parsed.ToolResults); matchedSession != nil {
				session = matchedSession
				hasSession = true
				matchedSessionKey = matchedKey
				session.resuming = true
				log.Debugf("cursor: matched tool result lineage to session %s", matchedKey)
			}
		}
		if hasSession && session.conversationID != "" {
			conversationId = session.conversationID
			sessionKey = authID + ":" + conversationId
			checkpointKey = conversationId
		}
		// If no session found for current auth, check for stale sessions from
		// a different auth on the same conversation (quota failover scenario).
		// Clean them up since the H2 stream belongs to the old account.
		if !hasSession {
			if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
				oldSession := e.sessions[oldKey]
				log.Infof("cursor: cleaning up stale session from auth %s for conv=%s (auth migrated to %s)", oldSession.authID, conversationId, authID)
				oldSession.cancel()
				if oldSession.stream != nil {
					oldSession.stream.Close()
				}
				delete(e.sessions, oldKey)
			}
		}
		e.mu.Unlock()

		if hasSession && session.stream != nil && session.authID == authID {
			continuationPending = append([]pendingMcpExec(nil), session.pending...)
			continuationMatched = true
			log.Debugf("cursor: resuming existing Run %s with %d tool results", matchedSessionKey, len(parsed.ToolResults))
			resumed, resumeErr := e.resumeWithToolResults(ctx, session, parsed.ToolResults)
			e.mu.Lock()
			if current := e.sessions[matchedSessionKey]; current == session {
				delete(e.sessions, matchedSessionKey)
			}
			e.mu.Unlock()
			if resumeErr == nil {
				return resumed, nil
			}
			if ctx.Err() != nil {
				closeCursorSession(session)
				return nil, fmt.Errorf("cursor: resume canceled: %w", ctx.Err())
			}
			log.Warnf("cursor: existing Run %s could not resume: %v; falling back to checkpoint continuation", matchedSessionKey, resumeErr)
			closeCursorSession(session)
		}
		if hasSession && session.authID != authID {
			log.Warnf("cursor: session %s belongs to auth %s, but request is from %s — skipping resume", matchedSessionKey, session.authID, authID)
		}
	}

	// Clean up any stale session for this key (or from a previous auth on same conversation)
	e.mu.Lock()
	if old, ok := e.sessions[sessionKey]; ok {
		old.cancel()
		delete(e.sessions, sessionKey)
	} else if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
		old := e.sessions[oldKey]
		old.cancel()
		if old.stream != nil {
			old.stream.Close()
		}
		delete(e.sessions, oldKey)
	}
	e.mu.Unlock()

	hadConversationHistory := len(parsed.ToolResults) > 0 || len(parsed.Turns) > 0

	// Look up saved checkpoint for this conversation (keyed by conversationId only).
	// Checkpoint is auth-specific: if auth changed (e.g. quota exhaustion failover),
	// the old checkpoint is useless on the new account — discard and flatten.
	e.mu.Lock()
	saved, hasCheckpoint := cloneSavedCursorCheckpoint(e.checkpoints[checkpointKey])
	e.mu.Unlock()

	useCheckpoint := hasCheckpoint && saved != nil && len(saved.data) > 0 && saved.authID == authID
	if useCheckpoint && len(parsed.ToolResults) > 0 {
		if !continuationMatched || !prepareCursorCheckpointContinuation(parsed, continuationPending) {
			// A checkpoint without exact pending tool lineage may belong to another
			// concurrent branch sharing a client session ID. Prefer a cold replay.
			useCheckpoint = false
			log.Debugf("cursor: checkpoint lacks matching tool lineage for conv=%s; flattening safely", checkpointKey)
		}
	}

	params := buildRunRequestParams(parsed, conversationId, req.Model)

	if useCheckpoint {
		log.Debugf("cursor: using saved checkpoint (%d bytes) for conv=%s auth=%s delta=%t", len(saved.data), checkpointKey, authID, continuationMatched)
		params.RawCheckpoint = saved.data
		for k, v := range saved.blobStore {
			params.BlobStore[k] = append([]byte(nil), v...)
		}
	} else if hasCheckpoint && saved.data != nil && saved.authID != authID {
		// Auth changed (quota failover) — checkpoint is not portable across accounts.
		// Discard and flatten conversation history into userText.
		log.Infof("cursor: auth migrated (%s → %s) for conv=%s, discarding checkpoint and flattening context", saved.authID, authID, checkpointKey)
		e.mu.Lock()
		delete(e.checkpoints, checkpointKey)
		e.mu.Unlock()
		if hadConversationHistory {
			flattenConversationIntoUserText(parsed)
			params = buildRunRequestParams(parsed, conversationId, req.Model)
		}
	} else if hadConversationHistory {
		// Fallback: no checkpoint available (cold resume / proxy restart).
		// Flatten the full conversation history (including tool interactions) into userText.
		// Cursor's turns encoding is not reliably read by the model, but userText always works.
		log.Debugf("cursor: no checkpoint, flattening %d turns + %d tool results into userText", len(parsed.Turns), len(parsed.ToolResults))
		flattenConversationIntoUserText(parsed)
		params = buildRunRequestParams(parsed, conversationId, req.Model)
	}
	requestBytes := cursorproto.EncodeRunRequest(params)
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)
	log.Debugf("cursor: encoded Run request bytes=%d userTextBytes=%d checkpoint=%t conv=%s", len(requestBytes), len(params.UserText), len(params.RawCheckpoint) > 0, conversationId)

	requestStartedAt := time.Now()
	stream, err := e.openCursorH2Stream(ctx, auth, accessToken)
	if err != nil {
		return nil, err
	}

	if err := stream.Write(framedRequest); err != nil {
		stream.Close()
		return nil, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	// Use a session-scoped context for the heartbeat that is NOT tied to the HTTP request.
	// This ensures the heartbeat survives across request boundaries during MCP tool execution.
	// Mirrors the TS plugin's setInterval-based heartbeat that lives independently of HTTP responses.
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	go cursorH2Heartbeat(sessionCtx, stream)
	e.closeStreamWhenDownstreamEnds(ctx, stream, sessionCancel)

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	chatId := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()

	var streamParam any

	// Keep the upstream Run alive across client-side tool execution. Each downstream
	// HTTP response gets its own output channel, while tool results share this channel.
	toolResultCh := make(chan []toolResultInfo, 1)
	streamDone := make(chan struct{})

	// The output closes at an MCP tool boundary while the upstream stream remains
	// available for checkpoint and KV messages.
	var outMu sync.Mutex
	currentOut := chunks

	emitToOut := func(chunk cliproxyexecutor.StreamChunk) {
		outMu.Lock()
		out := currentOut
		outMu.Unlock()
		if out != nil {
			out <- chunk
		}
	}

	// Wrap sendChunk/sendDone to use emitToOut
	sendChunkSwitchable := func(delta string, finishReason string) {
		fr := "null"
		if finishReason != "" {
			fr = finishReason
		}
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
			chatId, created, parsed.Model, delta, fr)
		sseLine := []byte("data: " + openaiJSON + "\n")

		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: normalizeCursorResponsesUsage(from, bytes.Clone(t))})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
	}

	sendDoneSwitchable := func() {
		if needsTranslate {
			done := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, []byte("data: [DONE]\n"), &streamParam)
			for _, d := range done {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: normalizeCursorResponsesUsage(from, bytes.Clone(d))})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte("[DONE]")})
		}
	}

	// Pre-response error detection for transparent failover:
	// If the stream fails before any chunk is emitted (e.g. quota exceeded),
	// ExecuteStream returns an error so the conductor retries with a different auth.
	streamErrCh := make(chan error, 1)
	firstChunkSent := make(chan struct{}, 1) // buffered: goroutine won't block signaling

	origEmitToOut := emitToOut
	var firstChunkLogOnce sync.Once
	emitToOut = func(chunk cliproxyexecutor.StreamChunk) {
		firstChunkLogOnce.Do(func() {
			log.Debugf("cursor: first downstream chunk after %s conv=%s checkpoint=%t", time.Since(requestStartedAt).Round(time.Millisecond), conversationId, len(params.RawCheckpoint) > 0)
		})
		select {
		case firstChunkSent <- struct{}{}:
		default:
		}
		origEmitToOut(chunk)
	}

	go func() {
		defer close(streamDone)
		roleSent := false
		toolCallIndex := 0
		usage := &cursorTokenUsage{}
		usage.setInputEstimate(len(payload))

		streamErr := processH2SessionFrames(sessionCtx, stream, params.BlobStore, params.McpTools,
			func(text string, isThinking bool) {
				// Split thinking into reasoning_content (Codex/OpenAI-compat style).
				// Do not wrap thinking in <think> tags inside content.
				if isThinking {
					if !roleSent {
						roleSent = true
						sendChunkSwitchable(fmt.Sprintf(`{"role":"assistant","reasoning_content":%s}`, jsonString(text)), "")
					} else {
						sendChunkSwitchable(fmt.Sprintf(`{"reasoning_content":%s}`, jsonString(text)), "")
					}
					return
				}
				if !roleSent {
					roleSent = true
					sendChunkSwitchable(fmt.Sprintf(`{"role":"assistant","content":%s}`, jsonString(text)), "")
				} else {
					sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
				}
			},
			func(execs []pendingMcpExec) {
				for _, exec := range execs {
					toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":"%s","type":"function","function":{"name":"%s","arguments":%s}}]}`,
						toolCallIndex, exec.ToolCallId, exec.ToolName, jsonString(exec.Args))
					toolCallIndex++
					if !roleSent {
						roleSent = true
						// Tool-only first emission still needs an assistant role.
						sendChunkSwitchable(fmt.Sprintf(`{"role":"assistant","tool_calls":[{"index":%d,"id":"%s","type":"function","function":{"name":"%s","arguments":%s}}]}`,
							toolCallIndex-1, exec.ToolCallId, exec.ToolName, jsonString(exec.Args)), "")
					} else {
						sendChunkSwitchable(toolCallJSON, "")
					}
				}
				sendChunkSwitchable(`{}`, `"tool_calls"`)
				sendDoneSwitchable()

				// Register the pending tool call before closing the current HTTP
				// response. The downstream cancellation watcher can then distinguish a
				// normal tool boundary from a user cancellation without a race.
				resumeOut := make(chan cliproxyexecutor.StreamChunk, 64)
				log.Debugf("cursor: saving session %s for inline tool resume (tools=%d)", sessionKey, len(execs))
				e.mu.Lock()
				e.sessions[sessionKey] = &cursorSession{
					stream:       stream,
					pending:      append([]pendingMcpExec(nil), execs...),
					toolResultCh: toolResultCh,
					resumeOutCh:  resumeOut,
					switchOutput: func(ch chan cliproxyexecutor.StreamChunk) {
						outMu.Lock()
						currentOut = ch
						streamParam = nil
						chatId = "chatcmpl-" + uuid.New().String()[:28]
						created = time.Now().Unix()
						roleSent = false
						toolCallIndex = 0
						outMu.Unlock()
					},
					streamDone:     streamDone,
					cancel:         sessionCancel,
					createdAt:      time.Now(),
					authID:         authID,
					conversationID: conversationId,
				}
				e.mu.Unlock()

				// Close current output to end the current HTTP SSE response.
				outMu.Lock()
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()

				// processH2SessionFrames now waits while continuing to handle checkpoint,
				// KV, and heartbeat messages. It is canceled by the fresh continuation.
			},
			toolResultCh,
			usage,
			func(cpData []byte) {
				// Save checkpoint keyed by conversationId, tagged with authID for migration detection
				e.mu.Lock()
				e.checkpoints[checkpointKey] = &savedCheckpoint{
					data:      append([]byte(nil), cpData...),
					blobStore: cloneCursorBlobStore(params.BlobStore),
					authID:    authID,
					updatedAt: time.Now(),
				}
				e.mu.Unlock()
				log.Debugf("cursor: saved checkpoint (%d bytes) for conv=%s auth=%s", len(cpData), checkpointKey, authID)
			},
		)

		// processH2SessionFrames returned — stream is done.
		// Check if error happened before any chunks were emitted.
		if streamErr != nil {
			select {
			case <-firstChunkSent:
				// Chunks were already sent to client — can't transparently retry.
				// Next request will failover via conductor's cooldown mechanism.
				log.Warnf("cursor: stream error after data sent (auth=%s conv=%s): %v", authID, conversationId, streamErr)
			default:
				// No data sent yet — propagate error for transparent conductor retry.
				log.Warnf("cursor: stream error before data sent (auth=%s conv=%s): %v — signaling retry", authID, conversationId, streamErr)
				streamErrCh <- streamErr
				outMu.Lock()
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()
				sessionCancel()
				stream.Close()
				return
			}
		}

		// Include token usage in the final stop chunk
		inputTok, outputTok := usage.get()
		// Build the stop chunk with usage embedded in the choices array level
		fr := `"stop"`
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":%s}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
			chatId, created, parsed.Model, fr, inputTok, outputTok, inputTok+outputTok)
		sseLine := []byte("data: " + openaiJSON + "\n")
		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: normalizeCursorResponsesUsage(from, bytes.Clone(t))})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
		sendDoneSwitchable()

		// Close whatever output channel is still active
		outMu.Lock()
		if currentOut != nil {
			close(currentOut)
			currentOut = nil
		}
		outMu.Unlock()
		sessionCancel()
		stream.Close()
	}()

	// Wait for either the first chunk or a pre-response error.
	// If the stream fails before emitting any data (e.g. quota exceeded),
	// return an error so the conductor retries with a different auth.
	select {
	case streamErr := <-streamErrCh:
		return nil, classifyCursorError(fmt.Errorf("cursor: stream failed before response: %w", streamErr))
	case <-firstChunkSent:
		// Data started flowing — return stream to client
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
}

func (e *CursorExecutor) resumeWithToolResults(ctx context.Context, session *cursorSession, results []toolResultInfo) (*cliproxyexecutor.StreamResult, error) {
	if session == nil || session.toolResultCh == nil {
		return nil, fmt.Errorf("session has no tool result channel")
	}
	if session.resumeOutCh == nil || session.switchOutput == nil {
		return nil, fmt.Errorf("session has no resume output")
	}

	session.switchOutput(session.resumeOutCh)
	select {
	case session.toolResultCh <- results:
		if session.stream != nil {
			e.closeStreamWhenDownstreamEnds(ctx, session.stream, session.cancel)
		}
		return &cliproxyexecutor.StreamResult{Chunks: session.resumeOutCh}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.streamDone:
		return nil, fmt.Errorf("upstream Run ended before tool result delivery")
	}
}

func normalizeCursorResponsesUsage(format sdktranslator.Format, payload []byte) []byte {
	if format == sdktranslator.FormatOpenAIResponse {
		return helps.EnsureResponsesUsageDetails(payload)
	}
	return payload
}

// --- H2Stream helpers ---

func openCursorH2Stream(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, accessToken string) (*cursorproto.H2Stream, error) {
	return openCursorH2StreamWithPool(ctx, cursorproto.NewH2StreamPool(), cfg, auth, accessToken)
}

func (e *CursorExecutor) openCursorH2Stream(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) (*cursorproto.H2Stream, error) {
	if e.h2Pool == nil {
		e.h2Pool = cursorproto.NewH2StreamPool()
	}
	return openCursorH2StreamWithPool(ctx, e.h2Pool, e.cfg, auth, accessToken)
}

func openCursorH2StreamWithPool(ctx context.Context, pool *cursorproto.H2StreamPool, cfg *config.Config, auth *cliproxyauth.Auth, accessToken string) (*cursorproto.H2Stream, error) {
	headers := cursorRequestHeaders(accessToken, cursorRunPath)
	proxyURL := cursorProxyURL(cfg, auth)
	dialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
	if errBuild != nil {
		return nil, fmt.Errorf("cursor: configure proxy: %w", errBuild)
	}
	if mode == proxyutil.ModeProxy {
		log.Debugf("cursor: opening H2 stream through proxy %s", proxyutil.Redact(proxyURL))
	}
	authKey := "anonymous"
	if auth != nil && strings.TrimSpace(auth.ID) != "" {
		authKey = strings.TrimSpace(auth.ID)
	} else if accessToken != "" {
		digest := sha256.Sum256([]byte(accessToken))
		authKey = hex.EncodeToString(digest[:8])
	}
	poolKey := strings.Join([]string{"api2.cursor.sh", proxyURL, authKey}, "\x00")
	return pool.Open(ctx, poolKey, "api2.cursor.sh", headers, dialer)
}

func cursorProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// cursorRequestHeaders mirrors Cursor IDE transport headers (not the CLI surface).
// IDE uses client-type "ide" and can fall back to the slow request pool; "cli"
// is often limited to the fast-request pool after quota exhaustion.
func cursorRequestHeaders(accessToken, path string) map[string]string {
	reqID := uuid.New().String()
	h := map[string]string{
		"content-type":                "application/connect+proto",
		"connect-protocol-version":    "1",
		"te":                          "trailers",
		"authorization":               "Bearer " + accessToken,
		"x-ghost-mode":                "true",
		"x-cursor-client-version":     cursorClientVersion,
		"x-cursor-client-commit":      cursorClientCommit,
		"x-cursor-client-type":        cursorClientType,
		"x-cursor-client-device-type": "desktop",
		"x-cursor-client-os":          "win32",
		"x-cursor-client-arch":        "x64",
		"x-cursor-checksum":           cursorChecksumHeader(),
		"x-request-id":                reqID,
		"x-amzn-trace-id":             "Root=" + reqID,
	}
	if path != "" {
		h[":path"] = path
	}
	return h
}

// cursorChecksumHeader builds the IDE-style x-cursor-checksum value:
// base64(time-obfuscated 6-byte timestamp) + machineId [/ macMachineId].
// Algorithm mirrors Cursor's alwaysLocalSingleton transport header helper.
func cursorChecksumHeader() string {
	// Stable synthetic IDs keep the header well-formed without binding to a host install.
	machineID := "cliproxy-cursor-machine"
	macMachineID := "cliproxy-cursor-mac-machine"
	ts := time.Now().UnixMilli() / 1_000_000
	raw := []byte{
		byte(ts >> 40), byte(ts >> 32), byte(ts >> 24),
		byte(ts >> 16), byte(ts >> 8), byte(ts),
	}
	n := byte(165)
	for i := 0; i < len(raw); i++ {
		raw[i] = (raw[i] ^ n) + byte(i%256)
		n = raw[i]
	}
	prefix := base64.StdEncoding.EncodeToString(raw)
	return prefix + machineID + "/" + macMachineID
}

func cursorH2Heartbeat(ctx context.Context, stream *cursorproto.H2Stream) {
	ticker := time.NewTicker(cursorHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := cursorproto.EncodeHeartbeat()
			frame := cursorproto.FrameConnectMessage(hb, 0)
			if err := stream.Write(frame); err != nil {
				return
			}
		}
	}
}

// --- Response processing ---

// cursorTokenUsage tracks token counts from Cursor's TokenDeltaUpdate messages.
type cursorTokenUsage struct {
	mu             sync.Mutex
	outputTokens   int64
	inputTokensEst int64 // estimated from request payload size
}

func (u *cursorTokenUsage) addOutput(delta int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.outputTokens += delta
}

func (u *cursorTokenUsage) setInputEstimate(payloadBytes int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Rough estimate: ~4 bytes per token for mixed content
	u.inputTokensEst = int64(payloadBytes / 4)
	if u.inputTokensEst < 1 {
		u.inputTokensEst = 1
	}
}

func (u *cursorTokenUsage) get() (input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inputTokensEst, u.outputTokens
}

func processH2SessionFrames(
	ctx context.Context,
	stream *cursorproto.H2Stream,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	onText func(text string, isThinking bool),
	onToolExec func(execs []pendingMcpExec),
	toolResultCh <-chan []toolResultInfo, // nil for no tool result injection; non-nil to wait for results
	tokenUsage *cursorTokenUsage, // tracks accumulated token usage (may be nil)
	onCheckpoint func(data []byte), // called when server sends conversation_checkpoint_update
) error {
	var buf bytes.Buffer
	rejectReason := "Tool not available in this environment. Use the MCP tools provided instead."
	log.Debugf("cursor: processH2SessionFrames started for streamID=%s, waiting for data...", stream.ID())
	for {
		select {
		case <-ctx.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: context done")
			return ctx.Err()
		case data, ok := <-stream.Data():
			if !ok {
				log.Debugf("cursor: processH2SessionFrames[%s]: exiting: stream data channel closed", stream.ID())
				return stream.Err() // may be RST_STREAM, GOAWAY, or nil for clean close
			}
			buf.Write(data)

			var pendingBatch []pendingMcpExec
			turnEnded := false

			// Process all complete frames already delivered in this H2 data event. Cursor
			// commonly batches independent native tool calls, so expose the full batch in
			// one downstream assistant turn instead of serializing it into extra Runs.
		collectBatch:
			for {
				for {
					currentBuf := buf.Bytes()
					if len(currentBuf) == 0 {
						break
					}
					flags, payload, consumed, ok := cursorproto.ParseConnectFrame(currentBuf)
					if !ok {
						break
					}
					buf.Next(consumed)

					if flags&cursorproto.ConnectEndStreamFlag != 0 {
						if err := cursorproto.ParseConnectEndStream(payload); err != nil {
							log.Warnf("cursor: connect end stream error: %v", err)
							return err // propagate server-side errors (quota, rate limit, etc.)
						}
						continue
					}

					msg, err := cursorproto.DecodeAgentServerMessage(payload)
					if err != nil {
						log.Debugf("cursor: failed to decode server message: %v", err)
						continue
					}

					switch msg.Type {
					case cursorproto.ServerMsgTextDelta:
						if msg.Text != "" && onText != nil {
							onText(msg.Text, false)
						}
					case cursorproto.ServerMsgThinkingDelta:
						if msg.Text != "" && onText != nil {
							onText(msg.Text, true)
						}
					case cursorproto.ServerMsgThinkingCompleted:
						// Handled by caller

					case cursorproto.ServerMsgTurnEnded:
						log.Debugf("cursor: TurnEnded received, stream will finish")
						turnEnded = true

					case cursorproto.ServerMsgHeartbeat:
						// Server heartbeat, ignore silently
						continue

					case cursorproto.ServerMsgCheckpoint:
						if onCheckpoint != nil && len(msg.CheckpointData) > 0 {
							onCheckpoint(msg.CheckpointData)
						}
						continue

					case cursorproto.ServerMsgTokenDelta:
						if tokenUsage != nil && msg.TokenDelta > 0 {
							tokenUsage.addOutput(msg.TokenDelta)
						}
						continue

					case cursorproto.ServerMsgKvGetBlob:
						blobKey := cursorproto.BlobIdHex(msg.BlobId)
						data := blobStore[blobKey]
						resp := cursorproto.EncodeKvGetBlobResult(msg.KvId, data)
						stream.Write(cursorproto.FrameConnectMessage(resp, 0))

					case cursorproto.ServerMsgKvSetBlob:
						blobKey := cursorproto.BlobIdHex(msg.BlobId)
						blobStore[blobKey] = append([]byte(nil), msg.BlobData...)
						resp := cursorproto.EncodeKvSetBlobResult(msg.KvId)
						stream.Write(cursorproto.FrameConnectMessage(resp, 0))

					case cursorproto.ServerMsgExecRequestCtx:
						resp := cursorproto.EncodeExecRequestContextResult(msg.ExecMsgId, msg.ExecId, mcpTools)
						if err := writeCursorExecMessages(stream, appendCursorExecClose([][]byte{resp}, msg.ExecMsgId)); err != nil {
							return fmt.Errorf("cursor: write request context result: %w", err)
						}

					case cursorproto.ServerMsgExecMcpArgs:
						if onToolExec != nil {
							decodedArgs := decodeMcpArgsToJSON(msg.McpArgs)
							if rejection := rejectCursorTeammateTaskOutput(msg.McpToolName, decodedArgs); rejection != "" {
								messages := [][]byte{cursorproto.EncodeExecMcpError(msg.ExecMsgId, msg.ExecId, rejection)}
								if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
									return fmt.Errorf("cursor: reject invalid teammate TaskOutput: %w", err)
								}
								log.Debugf("cursor: rejected TaskOutput with teammate agent id execMsgId=%d", msg.ExecMsgId)
								continue
							}
							toolCallId := msg.McpToolCallId
							if toolCallId == "" {
								toolCallId = uuid.New().String()
							}
							log.Debugf("cursor: received mcpArgs from server: execMsgId=%d execId=%q toolName=%s toolCallId=%s",
								msg.ExecMsgId, msg.ExecId, msg.McpToolName, toolCallId)
							pending := pendingMcpExec{
								ExecMsgId:  msg.ExecMsgId,
								ExecId:     msg.ExecId,
								ToolCallId: toolCallId,
								ToolName:   msg.McpToolName,
								Args:       decodedArgs,
								Kind:       cursorExecMCP,
							}
							pendingBatch = append(pendingBatch, pending)
						} else {
							messages := [][]byte{cursorproto.EncodeExecMcpError(msg.ExecMsgId, msg.ExecId, rejectReason)}
							if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
								return err
							}
						}

					case cursorproto.ServerMsgExecReadArgs, cursorproto.ServerMsgExecWriteArgs,
						cursorproto.ServerMsgExecDeleteArgs, cursorproto.ServerMsgExecLsArgs,
						cursorproto.ServerMsgExecGrepArgs, cursorproto.ServerMsgExecShellArgs,
						cursorproto.ServerMsgExecShellStream, cursorproto.ServerMsgExecFetchArgs:
						if pending, ok := bridgeCursorNativeExec(msg, mcpTools); ok && onToolExec != nil {
							pendingBatch = append(pendingBatch, pending)
						} else if err := writeCursorExecMessages(stream, encodeCursorExecRejection(msg, rejectReason)); err != nil {
							return fmt.Errorf("cursor: reject native exec: %w", err)
						}
					case cursorproto.ServerMsgExecBgShellSpawn:
						messages := [][]byte{cursorproto.EncodeExecBackgroundShellSpawnRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)}
						if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
							return err
						}
					case cursorproto.ServerMsgExecDiagnostics:
						messages := [][]byte{cursorproto.EncodeExecDiagnosticsResult(msg.ExecMsgId, msg.ExecId)}
						if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
							return err
						}
					case cursorproto.ServerMsgExecWriteShellStdin:
						messages := [][]byte{cursorproto.EncodeExecWriteShellStdinError(msg.ExecMsgId, msg.ExecId, rejectReason)}
						if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
							return err
						}
					case cursorproto.ServerMsgExecOther:
						if err := writeCursorExecMessages(stream, [][]byte{cursorproto.EncodeExecStreamClose(msg.ExecMsgId)}); err != nil {
							return err
						}
					}
				}

				if len(pendingBatch) == 0 {
					break collectBatch
				}
				select {
				case moreData, ok := <-stream.Data():
					if !ok {
						return cursorStreamClosedError(stream)
					}
					buf.Write(moreData)
					continue collectBatch
				default:
					break collectBatch
				}
			}

			for len(pendingBatch) > 0 {
				onToolExec(append([]pendingMcpExec(nil), pendingBatch...))
				if toolResultCh == nil {
					return nil
				}
				log.Debugf("cursor: waiting for %d downstream tool results on active Run", len(pendingBatch))
				toolResults, queuedBatch, err := waitForCursorToolResults(ctx, stream, &buf, blobStore, mcpTools, toolResultCh, tokenUsage, onCheckpoint)
				if err != nil {
					return err
				}
				for _, pending := range pendingBatch {
					result, ok := findCursorToolResult(toolResults, pending.ToolCallId)
					if !ok {
						result = toolResultInfo{
							ToolCallId: pending.ToolCallId,
							Content:    "The downstream client did not return a result for this tool call.",
							IsError:    true,
						}
					}
					if err := writeCursorExecMessages(stream, encodeCursorExecCompletion(pending, result)); err != nil {
						return fmt.Errorf("cursor: write tool result for %s: %w", pending.ToolName, err)
					}
					log.Debugf("cursor: completed inline exec id=%d tool=%s nativeKind=%d error=%t", pending.ExecMsgId, pending.ToolName, pending.Kind, result.IsError)
				}
				if len(queuedBatch) > 0 {
					log.Debugf("cursor: delivering %d tool calls queued behind the completed downstream batch", len(queuedBatch))
				}
				// Cursor may dispatch more client tools while earlier ones are still
				// running. Claude clients require each assistant tool batch to finish
				// before returning its results, so expose queued calls in the next turn.
				pendingBatch = queuedBatch
			}
			if turnEnded {
				return nil
			}

		case <-stream.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: stream done")
			return stream.Err()
		}
	}
}

type cursorMessageWriter interface {
	Write([]byte) error
}

type cursorToolResultStream interface {
	cursorMessageWriter
	Data() <-chan []byte
	Done() <-chan struct{}
	Err() error
}

func waitForCursorToolResults(
	ctx context.Context,
	stream cursorToolResultStream,
	buf *bytes.Buffer,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	toolResultCh <-chan []toolResultInfo,
	tokenUsage *cursorTokenUsage,
	onCheckpoint func(data []byte),
) ([]toolResultInfo, []pendingMcpExec, error) {
	rejectReason := "Tool not available in this environment. Use the MCP tools provided instead."
	// Do not reject supported execs that arrive while the downstream client is
	// executing the current batch. They belong to a later Claude assistant turn.
	var queuedBatch []pendingMcpExec
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case results, ok := <-toolResultCh:
			if !ok {
				return nil, nil, fmt.Errorf("cursor: downstream tool result channel closed")
			}
			return results, queuedBatch, nil
		case waitData, ok := <-stream.Data():
			if !ok {
				return nil, nil, cursorStreamClosedError(stream)
			}
			buf.Write(waitData)
			for {
				frame := buf.Bytes()
				if len(frame) == 0 {
					break
				}
				flags, payload, consumed, complete := cursorproto.ParseConnectFrame(frame)
				if !complete {
					break
				}
				buf.Next(consumed)
				if flags&cursorproto.ConnectEndStreamFlag != 0 {
					if err := cursorproto.ParseConnectEndStream(payload); err != nil {
						return nil, nil, err
					}
					continue
				}
				msg, err := cursorproto.DecodeAgentServerMessage(payload)
				if err != nil {
					continue
				}
				switch msg.Type {
				case cursorproto.ServerMsgKvGetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					if err := stream.Write(cursorproto.FrameConnectMessage(cursorproto.EncodeKvGetBlobResult(msg.KvId, blobStore[blobKey]), 0)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgKvSetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					blobStore[blobKey] = append([]byte(nil), msg.BlobData...)
					if err := stream.Write(cursorproto.FrameConnectMessage(cursorproto.EncodeKvSetBlobResult(msg.KvId), 0)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecRequestCtx:
					messages := [][]byte{cursorproto.EncodeExecRequestContextResult(msg.ExecMsgId, msg.ExecId, mcpTools)}
					if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecMcpArgs:
					decodedArgs := decodeMcpArgsToJSON(msg.McpArgs)
					if rejection := rejectCursorTeammateTaskOutput(msg.McpToolName, decodedArgs); rejection != "" {
						messages := [][]byte{cursorproto.EncodeExecMcpError(msg.ExecMsgId, msg.ExecId, rejection)}
						if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
							return nil, nil, err
						}
						continue
					}
					toolCallID := msg.McpToolCallId
					if toolCallID == "" {
						toolCallID = uuid.New().String()
					}
					queuedBatch = append(queuedBatch, pendingMcpExec{
						ExecMsgId:  msg.ExecMsgId,
						ExecId:     msg.ExecId,
						ToolCallId: toolCallID,
						ToolName:   msg.McpToolName,
						Args:       decodedArgs,
						Kind:       cursorExecMCP,
					})
				case cursorproto.ServerMsgExecReadArgs, cursorproto.ServerMsgExecWriteArgs,
					cursorproto.ServerMsgExecDeleteArgs, cursorproto.ServerMsgExecLsArgs,
					cursorproto.ServerMsgExecGrepArgs, cursorproto.ServerMsgExecShellArgs,
					cursorproto.ServerMsgExecShellStream, cursorproto.ServerMsgExecFetchArgs:
					if pending, bridged := bridgeCursorNativeExec(msg, mcpTools); bridged {
						queuedBatch = append(queuedBatch, pending)
					} else if err := writeCursorExecMessages(stream, encodeCursorExecRejection(msg, rejectReason)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecDiagnostics:
					messages := [][]byte{cursorproto.EncodeExecDiagnosticsResult(msg.ExecMsgId, msg.ExecId)}
					if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecBgShellSpawn:
					messages := [][]byte{cursorproto.EncodeExecBackgroundShellSpawnRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)}
					if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecWriteShellStdin:
					messages := [][]byte{cursorproto.EncodeExecWriteShellStdinError(msg.ExecMsgId, msg.ExecId, rejectReason)}
					if err := writeCursorExecMessages(stream, appendCursorExecClose(messages, msg.ExecMsgId)); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgExecOther:
					if err := writeCursorExecMessages(stream, [][]byte{cursorproto.EncodeExecStreamClose(msg.ExecMsgId)}); err != nil {
						return nil, nil, err
					}
				case cursorproto.ServerMsgCheckpoint:
					if onCheckpoint != nil && len(msg.CheckpointData) > 0 {
						onCheckpoint(msg.CheckpointData)
					}
				case cursorproto.ServerMsgTokenDelta:
					if tokenUsage != nil && msg.TokenDelta > 0 {
						tokenUsage.addOutput(msg.TokenDelta)
					}
				case cursorproto.ServerMsgTurnEnded:
					return nil, nil, fmt.Errorf("cursor: Run ended before downstream tool results were returned")
				}
			}
		case <-stream.Done():
			return nil, nil, cursorStreamClosedError(stream)
		}
	}
}

func cursorStreamClosedError(stream cursorToolResultStream) error {
	if err := stream.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func writeCursorExecMessages(stream cursorMessageWriter, messages [][]byte) error {
	for _, message := range messages {
		if err := stream.Write(cursorproto.FrameConnectMessage(message, 0)); err != nil {
			return err
		}
	}
	return nil
}

func appendCursorExecClose(messages [][]byte, execMsgID uint32) [][]byte {
	return append(messages, cursorproto.EncodeExecStreamClose(execMsgID))
}

func findCursorToolResult(results []toolResultInfo, toolCallID string) (toolResultInfo, bool) {
	for index := len(results) - 1; index >= 0; index-- {
		if results[index].ToolCallId == toolCallID {
			return results[index], true
		}
	}
	return toolResultInfo{}, false
}

func encodeCursorExecCompletion(pending pendingMcpExec, result toolResultInfo) [][]byte {
	var messages [][]byte
	switch pending.Kind {
	case cursorExecMCP:
		messages = append(messages, cursorproto.EncodeExecMcpResult(pending.ExecMsgId, pending.ExecId, result.Content, result.IsError))
	case cursorExecShell:
		messages = append(messages, cursorproto.EncodeExecShellResult(pending.ExecMsgId, pending.ExecId, pending.Command, pending.WorkDir, result.Content, result.IsError))
	case cursorExecShellStream:
		messages = append(messages, cursorproto.EncodeExecShellStreamResult(pending.ExecMsgId, pending.ExecId, pending.WorkDir, result.Content, result.IsError)...)
	case cursorExecRead:
		messages = append(messages, cursorproto.EncodeExecReadResult(pending.ExecMsgId, pending.ExecId, pending.Path, result.Content, result.IsError))
	case cursorExecWrite:
		messages = append(messages, cursorproto.EncodeExecWriteResult(pending.ExecMsgId, pending.ExecId, pending.Path, pending.FileText, result.Content, result.IsError))
	case cursorExecDelete:
		messages = append(messages, cursorproto.EncodeExecDeleteResult(pending.ExecMsgId, pending.ExecId, pending.Path, result.Content, result.IsError))
	case cursorExecLs:
		messages = append(messages, cursorproto.EncodeExecLsResult(pending.ExecMsgId, pending.ExecId, pending.Path, result.Content, result.IsError))
	case cursorExecGrep:
		messages = append(messages, cursorproto.EncodeExecGrepResult(pending.ExecMsgId, pending.ExecId, pending.Pattern, pending.Path, pending.OutputMode, result.Content, result.IsError))
	case cursorExecFetch:
		messages = append(messages, cursorproto.EncodeExecFetchResult(pending.ExecMsgId, pending.ExecId, pending.URL, result.Content, result.IsError))
	}
	return appendCursorExecClose(messages, pending.ExecMsgId)
}

func encodeCursorExecRejection(msg *cursorproto.DecodedServerMessage, reason string) [][]byte {
	var result []byte
	switch msg.Type {
	case cursorproto.ServerMsgExecReadArgs:
		result = cursorproto.EncodeExecReadRejected(msg.ExecMsgId, msg.ExecId, msg.Path, reason)
	case cursorproto.ServerMsgExecWriteArgs:
		result = cursorproto.EncodeExecWriteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, reason)
	case cursorproto.ServerMsgExecDeleteArgs:
		result = cursorproto.EncodeExecDeleteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, reason)
	case cursorproto.ServerMsgExecLsArgs:
		result = cursorproto.EncodeExecLsRejected(msg.ExecMsgId, msg.ExecId, msg.Path, reason)
	case cursorproto.ServerMsgExecGrepArgs:
		result = cursorproto.EncodeExecGrepError(msg.ExecMsgId, msg.ExecId, reason)
	case cursorproto.ServerMsgExecShellArgs:
		result = cursorproto.EncodeExecShellRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, reason)
	case cursorproto.ServerMsgExecShellStream:
		result = cursorproto.EncodeExecShellStreamRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, reason)
	case cursorproto.ServerMsgExecFetchArgs:
		result = cursorproto.EncodeExecFetchError(msg.ExecMsgId, msg.ExecId, msg.Url, reason)
	}
	if result == nil {
		return [][]byte{cursorproto.EncodeExecStreamClose(msg.ExecMsgId)}
	}
	return appendCursorExecClose([][]byte{result}, msg.ExecMsgId)
}

func bridgeCursorNativeExec(msg *cursorproto.DecodedServerMessage, tools []cursorproto.McpToolDef) (pendingMcpExec, bool) {
	if msg == nil {
		return pendingMcpExec{}, false
	}
	pending := pendingMcpExec{
		ExecMsgId:  msg.ExecMsgId,
		ExecId:     msg.ExecId,
		ToolCallId: msg.ToolCallId,
		Path:       msg.Path,
		Command:    msg.Command,
		WorkDir:    msg.WorkingDirectory,
		URL:        msg.Url,
		Pattern:    msg.Pattern,
		OutputMode: msg.OutputMode,
		FileText:   msg.FileText,
	}
	if pending.ToolCallId == "" {
		pending.ToolCallId = uuid.New().String()
	}
	if pending.OutputMode == "" {
		pending.OutputMode = "content"
	}

	var tool cursorproto.McpToolDef
	var args map[string]any
	var ok bool
	switch msg.Type {
	case cursorproto.ServerMsgExecShellArgs, cursorproto.ServerMsgExecShellStream:
		if msg.IsBackground || strings.TrimSpace(msg.Command) == "" {
			return pendingMcpExec{}, false
		}
		tool, ok = findCursorBridgeTool(tools, "Bash", "Shell")
		if !ok {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"command"}, msg.Command) {
			return pendingMcpExec{}, false
		}
		if msg.Timeout > 0 {
			setCursorToolArg(args, tool, []string{"timeout"}, msg.Timeout)
		}
		pending.Kind = cursorExecShell
		if msg.Type == cursorproto.ServerMsgExecShellStream {
			pending.Kind = cursorExecShellStream
		}
	case cursorproto.ServerMsgExecReadArgs:
		tool, ok = findCursorBridgeTool(tools, "Read")
		if !ok {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"file_path", "path"}, msg.Path) {
			return pendingMcpExec{}, false
		}
		pending.Kind = cursorExecRead
	case cursorproto.ServerMsgExecWriteArgs:
		fileText := msg.FileText
		if fileText == "" && len(msg.FileBytes) > 0 {
			if !utf8.Valid(msg.FileBytes) {
				return pendingMcpExec{}, false
			}
			fileText = string(msg.FileBytes)
			pending.FileText = fileText
		}
		tool, ok = findCursorBridgeTool(tools, "Write")
		if !ok {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"file_path", "path"}, msg.Path) ||
			!setCursorToolArg(args, tool, []string{"content", "file_text"}, fileText) {
			return pendingMcpExec{}, false
		}
		pending.Kind = cursorExecWrite
	case cursorproto.ServerMsgExecDeleteArgs:
		tool, ok = findCursorBridgeTool(tools, "Delete")
		if !ok {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"file_path", "path"}, msg.Path) {
			return pendingMcpExec{}, false
		}
		pending.Kind = cursorExecDelete
	case cursorproto.ServerMsgExecLsArgs:
		tool, ok = findCursorBridgeTool(tools, "Glob")
		if !ok {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"pattern"}, "*") {
			return pendingMcpExec{}, false
		}
		setCursorToolArg(args, tool, []string{"path"}, msg.Path)
		pending.Kind = cursorExecLs
	case cursorproto.ServerMsgExecGrepArgs:
		args = make(map[string]any)
		if strings.TrimSpace(msg.Pattern) == "" && strings.TrimSpace(msg.Glob) != "" && pending.OutputMode == "files_with_matches" {
			tool, ok = findCursorBridgeTool(tools, "Glob")
			if !ok || !setCursorToolArg(args, tool, []string{"pattern"}, msg.Glob) {
				return pendingMcpExec{}, false
			}
			setCursorToolArg(args, tool, []string{"path"}, msg.Path)
		} else {
			tool, ok = findCursorBridgeTool(tools, "Grep")
			if !ok || strings.TrimSpace(msg.Pattern) == "" || !setCursorToolArg(args, tool, []string{"pattern"}, msg.Pattern) {
				return pendingMcpExec{}, false
			}
			setCursorToolArg(args, tool, []string{"path"}, msg.Path)
			setCursorToolArg(args, tool, []string{"glob"}, msg.Glob)
			setCursorToolArg(args, tool, []string{"output_mode"}, pending.OutputMode)
			setCursorToolArg(args, tool, []string{"-B", "context_before"}, msg.ContextBefore)
			setCursorToolArg(args, tool, []string{"-A", "context_after"}, msg.ContextAfter)
			setCursorToolArg(args, tool, []string{"-C", "context"}, msg.Context)
			setCursorToolArg(args, tool, []string{"-i", "case_insensitive"}, msg.CaseInsensitive)
			setCursorToolArg(args, tool, []string{"type"}, msg.FileType)
			setCursorToolArg(args, tool, []string{"head_limit"}, msg.HeadLimit)
			setCursorToolArg(args, tool, []string{"multiline"}, msg.Multiline)
		}
		pending.Kind = cursorExecGrep
	case cursorproto.ServerMsgExecFetchArgs:
		tool, ok = findCursorBridgeTool(tools, "WebFetch", "Fetch")
		if !ok || strings.TrimSpace(msg.Url) == "" {
			return pendingMcpExec{}, false
		}
		args = make(map[string]any)
		if !setCursorToolArg(args, tool, []string{"url"}, msg.Url) {
			return pendingMcpExec{}, false
		}
		setCursorToolArg(args, tool, []string{"prompt"}, "Return the complete relevant content from this URL.")
		pending.Kind = cursorExecFetch
	default:
		return pendingMcpExec{}, false
	}

	if !cursorToolRequiredArgsPresent(tool, args) {
		return pendingMcpExec{}, false
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		return pendingMcpExec{}, false
	}
	pending.ToolName = tool.Name
	pending.Args = string(encodedArgs)
	return pending, true
}

func findCursorBridgeTool(tools []cursorproto.McpToolDef, aliases ...string) (cursorproto.McpToolDef, bool) {
	for _, alias := range aliases {
		for _, tool := range tools {
			if strings.EqualFold(strings.TrimSpace(tool.Name), alias) {
				return tool, true
			}
		}
	}
	return cursorproto.McpToolDef{}, false
}

func setCursorToolArg(args map[string]any, tool cursorproto.McpToolDef, aliases []string, value any) bool {
	properties := gjson.GetBytes(tool.InputSchema, "properties")
	for _, alias := range aliases {
		if !properties.Exists() || properties.Get(alias).Exists() {
			args[alias] = value
			return true
		}
	}
	return false
}

func cursorToolRequiredArgsPresent(tool cursorproto.McpToolDef, args map[string]any) bool {
	for _, required := range gjson.GetBytes(tool.InputSchema, "required").Array() {
		if _, ok := args[required.String()]; !ok {
			return false
		}
	}
	return true
}

// --- OpenAI request parsing ---

type parsedOpenAIRequest struct {
	Model        string
	Messages     []gjson.Result
	Tools        []gjson.Result
	Stream       bool
	SystemPrompt string
	UserText     string
	Images       []cursorproto.ImageData
	Turns        []cursorproto.TurnData
	ToolResults  []toolResultInfo
}

type toolResultInfo struct {
	ToolCallId string
	Content    string
	IsError    bool
}

func parseOpenAIRequest(payload []byte) *parsedOpenAIRequest {
	p := &parsedOpenAIRequest{
		Model:  gjson.GetBytes(payload, "model").String(),
		Stream: gjson.GetBytes(payload, "stream").Bool(),
	}

	messages := gjson.GetBytes(payload, "messages").Array()
	p.Messages = messages

	// Extract system prompt
	var systemParts []string
	for _, msg := range messages {
		if msg.Get("role").String() == "system" {
			systemParts = append(systemParts, extractTextContent(msg.Get("content")))
		}
	}
	if len(systemParts) > 0 {
		p.SystemPrompt = strings.Join(systemParts, "\n")
	} else {
		p.SystemPrompt = "You are a helpful assistant."
	}

	// Extract turns, tool results, and last user message
	var pendingUser string
	for _, msg := range messages {
		role := msg.Get("role").String()
		switch role {
		case "system":
			continue
		case "tool":
			p.ToolResults = append(p.ToolResults, toolResultInfo{
				ToolCallId: msg.Get("tool_call_id").String(),
				Content:    extractTextContent(msg.Get("content")),
			})
		case "user":
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{UserText: pendingUser})
			}
			pendingUser = extractTextContent(msg.Get("content"))
			p.Images = extractImages(msg.Get("content"))
		case "assistant":
			assistantText := extractTextContent(msg.Get("content"))
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{
					UserText:      pendingUser,
					AssistantText: assistantText,
				})
				pendingUser = ""
			} else if len(p.Turns) > 0 && assistantText != "" {
				// Assistant message after tool results (no pending user) —
				// append to the last turn's assistant text to preserve context.
				last := &p.Turns[len(p.Turns)-1]
				if last.AssistantText != "" {
					last.AssistantText += "\n" + assistantText
				} else {
					last.AssistantText = assistantText
				}
			}
		}
	}

	if pendingUser != "" {
		p.UserText = pendingUser
	} else if len(p.Turns) > 0 && len(p.ToolResults) == 0 {
		last := p.Turns[len(p.Turns)-1]
		p.Turns = p.Turns[:len(p.Turns)-1]
		p.UserText = last.UserText
	}

	// Extract tools
	p.Tools = gjson.GetBytes(payload, "tools").Array()

	return p
}

func applyOriginalToolResultErrors(parsed *parsedOpenAIRequest, originalPayload []byte) {
	if parsed == nil || len(parsed.ToolResults) == 0 || len(originalPayload) == 0 {
		return
	}
	errorByID := make(map[string]bool)
	for _, message := range gjson.GetBytes(originalPayload, "messages").Array() {
		if message.Get("role").String() == "tool" {
			toolCallID := message.Get("tool_call_id").String()
			if toolCallID != "" && message.Get("is_error").Bool() {
				errorByID[toolCallID] = true
			}
		}
		for _, block := range message.Get("content").Array() {
			if block.Get("type").String() != "tool_result" || !block.Get("is_error").Bool() {
				continue
			}
			toolCallID := block.Get("tool_use_id").String()
			if toolCallID != "" {
				errorByID[toolCallID] = true
			}
		}
	}
	for index := range parsed.ToolResults {
		parsed.ToolResults[index].IsError = errorByID[parsed.ToolResults[index].ToolCallId]
	}
}

func adaptCursorTeammateReply(parsed *parsedOpenAIRequest) {
	if parsed == nil || !strings.Contains(parsed.UserText, "<teammate-message") {
		return
	}
	hasSendMessage := cursorRequestHasTool(parsed, "SendMessage")
	hasToolSearch := cursorRequestHasTool(parsed, "ToolSearch")

	if strings.Contains(parsed.UserText, `"type":"idle_notification"`) || strings.Contains(parsed.UserText, `"type": "idle_notification"`) {
		parsed.UserText += "\n\nClaude Code team protocol: this is only a lifecycle notification; it does not contain the teammate's report. The teammate identifier is an Agent ID, not a TaskOutput task_id. Never call TaskOutput with an ID containing @session-. If the report is still needed, ask that teammate to send the complete report using SendMessage, then wait for a new teammate message that contains the report. Do not claim that the idle notification delivered a result."
		return
	}
	if !strings.Contains(parsed.UserText, `summary="`) {
		return
	}

	const deliveryRule = "Claude Code team protocol: a plain assistant response remains only in this teammate's transcript and is not delivered to the sender. "
	switch {
	case hasSendMessage:
		parsed.UserText += "\n\n" + deliveryRule + "Reply by calling the SendMessage tool exactly once, using the recipient requested in the teammate message and putting the complete conclusion in the tool content. Do not only print the conclusion as assistant text."
	case hasToolSearch:
		parsed.UserText += "\n\n" + deliveryRule + "SendMessage is deferred in this request. First call ToolSearch to load SendMessage, then call SendMessage exactly once using the recipient requested in the teammate message and put the complete conclusion in the tool content. Do not only print the conclusion as assistant text."
	default:
		parsed.UserText += "\n\n" + deliveryRule + "The required SendMessage tool is unavailable in this request, so do not claim the report was delivered and do not substitute TaskOutput. Return an explicit delivery-protocol error."
	}
}

func cursorRequestHasTool(parsed *parsedOpenAIRequest, name string) bool {
	if parsed == nil {
		return false
	}
	for _, tool := range parsed.Tools {
		if tool.Get("function.name").String() == name {
			return true
		}
	}
	return false
}

func rejectCursorTeammateTaskOutput(toolName, args string) string {
	if toolName != "TaskOutput" {
		return ""
	}
	taskID := strings.TrimSpace(gjson.Get(args, "task_id").String())
	if taskID == "" || !strings.Contains(taskID, "@session-") {
		return ""
	}
	return "TaskOutput rejected: the supplied value is a Claude Code teammate Agent ID, not a TaskOutput task_id. Ask the teammate to deliver its report with SendMessage; if SendMessage is deferred, load it with ToolSearch first."
}

// flattenConversationIntoUserText flattens the full conversation history
// into the UserText field as plain text while preserving tool call order.
// This is the fallback for cold resume when no checkpoint is available.
// Cursor reliably reads UserText but ignores structured turns.
func flattenConversationIntoUserText(parsed *parsedOpenAIRequest) {
	var buf strings.Builder
	currentUserIndex := -1
	if parsed.UserText != "" {
		for index := len(parsed.Messages) - 1; index >= 0; index-- {
			if parsed.Messages[index].Get("role").String() == "user" {
				currentUserIndex = index
				break
			}
		}
	}

	for index, message := range parsed.Messages {
		role := message.Get("role").String()
		switch role {
		case "system":
			continue
		case "user":
			if index == currentUserIndex {
				continue
			}
			appendCursorHistorySection(&buf, "USER", extractTextContent(message.Get("content")))
		case "assistant":
			appendCursorHistorySection(&buf, "ASSISTANT", extractTextContent(message.Get("content")))
			for _, toolCall := range message.Get("tool_calls").Array() {
				callID := toolCall.Get("id").String()
				name := toolCall.Get("function.name").String()
				arguments := truncateCursorHistoryText(toolCall.Get("function.arguments").String())
				fmt.Fprintf(&buf, "ASSISTANT_TOOL_CALL (call_id: %s, name: %s): %s\n\n", callID, name, arguments)
			}
		case "tool":
			callID := message.Get("tool_call_id").String()
			content := truncateCursorHistoryText(extractTextContent(message.Get("content")))
			fmt.Fprintf(&buf, "TOOL_RESULT (call_id: %s): %s\n\n", callID, content)
		}
	}

	if buf.Len() > 0 {
		buf.WriteString("The above is the previous conversation context including tool call results.\n")
		buf.WriteString("Continue your response based on this context.\n\n")
	}

	// Prepend flattened history to the current UserText
	if parsed.UserText != "" {
		parsed.UserText = buf.String() + "Current request: " + parsed.UserText
	} else {
		parsed.UserText = buf.String() + "Continue from the conversation above."
	}

	// Clear turns and tool results since they're now in UserText
	parsed.Turns = nil
	parsed.ToolResults = nil
}

func prepareCursorCheckpointContinuation(parsed *parsedOpenAIRequest, pending []pendingMcpExec) bool {
	if parsed == nil || len(pending) == 0 || len(parsed.ToolResults) == 0 {
		return false
	}

	latest := make(map[string]toolResultInfo, len(parsed.ToolResults))
	for _, result := range parsed.ToolResults {
		if result.ToolCallId != "" {
			latest[result.ToolCallId] = result
		}
	}

	var buf strings.Builder
	matched := 0
	for _, exec := range pending {
		result, ok := latest[exec.ToolCallId]
		if !ok {
			continue
		}
		content := truncateCursorHistoryText(result.Content)
		fmt.Fprintf(&buf, "TOOL_RESULT (call_id: %s, name: %s): %s\n\n", exec.ToolCallId, exec.ToolName, content)
		matched++
	}
	if matched == 0 {
		return false
	}
	if parsed.UserText != "" {
		buf.WriteString("CURRENT_USER_MESSAGE: ")
		buf.WriteString(strings.ToValidUTF8(parsed.UserText, "\uFFFD"))
		buf.WriteString("\n\n")
	}
	buf.WriteString("The tool results above complete the pending tool calls. Continue from the saved conversation state.")

	parsed.UserText = buf.String()
	parsed.Turns = nil
	parsed.ToolResults = nil
	return true
}

func appendCursorHistorySection(buf *strings.Builder, label, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	buf.WriteString(label)
	buf.WriteString(": ")
	buf.WriteString(truncateCursorHistoryText(content))
	buf.WriteString("\n\n")
}

func truncateCursorHistoryText(content string) string {
	const maxHistoryItemBytes = 8000
	content = strings.ToValidUTF8(content, "\uFFFD")
	if len(content) <= maxHistoryItemBytes {
		return content
	}
	cut := maxHistoryItemBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + "\n... [truncated]"
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				parts = append(parts, part.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return content.String()
}

func extractImages(content gjson.Result) []cursorproto.ImageData {
	if !content.IsArray() {
		return nil
	}
	var images []cursorproto.ImageData
	for _, part := range content.Array() {
		if part.Get("type").String() == "image_url" {
			url := part.Get("image_url.url").String()
			if strings.HasPrefix(url, "data:") {
				img := parseDataURL(url)
				if img != nil {
					images = append(images, *img)
				}
			}
		}
	}
	return images
}

func parseDataURL(url string) *cursorproto.ImageData {
	// data:image/png;base64,...
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	parts := strings.SplitN(url[5:], ";", 2)
	if len(parts) != 2 {
		return nil
	}
	mimeType := parts[0]
	if !strings.HasPrefix(parts[1], "base64,") {
		return nil
	}
	encoded := parts[1][7:]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Try RawStdEncoding for unpadded base64
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil
		}
	}
	return &cursorproto.ImageData{
		MimeType: mimeType,
		Data:     data,
	}
}

func buildRunRequestParams(parsed *parsedOpenAIRequest, conversationId, upstreamModel string) *cursorproto.RunRequestParams {
	// upstreamModel is the provider-resolved model name. Keep parsed.Model
	// unchanged so OpenAI-compatible responses continue to echo the client model.
	modelID := strings.TrimSpace(upstreamModel)
	if modelID == "" {
		modelID = parsed.Model
	}
	modelID, maxMode := normalizeCursorModel(modelID)

	params := &cursorproto.RunRequestParams{
		ModelId:        modelID,
		MaxMode:        maxMode,
		SystemPrompt:   parsed.SystemPrompt,
		UserText:       parsed.UserText,
		MessageId:      uuid.New().String(),
		ConversationId: conversationId,
		Images:         parsed.Images,
		Turns:          parsed.Turns,
		BlobStore:      make(map[string][]byte),
	}

	// Convert OpenAI tools to McpToolDefs
	for _, tool := range parsed.Tools {
		fn := tool.Get("function")
		params.McpTools = append(params.McpTools, cursorproto.McpToolDef{
			Name:        fn.Get("name").String(),
			Description: fn.Get("description").String(),
			InputSchema: json.RawMessage(fn.Get("parameters").Raw),
		})
	}

	return params
}

// normalizeCursorModel returns the upstream model id and whether Max Mode is required.
// Default max_mode is off so Normal-mode models can use the slow pool after fast
// quota exhaustion. cursor-grok-* currently requires Max Mode (ERROR_MAX_MODE_REQUIRED
// when false). Clients may force Max Mode with a "-maxmode" suffix (stripped upstream).
func normalizeCursorModel(modelID string) (string, bool) {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return id, false
	}
	lower := strings.ToLower(id)
	maxMode := false
	if strings.HasSuffix(lower, "-maxmode") {
		id = strings.TrimSpace(id[:len(id)-len("-maxmode")])
		lower = strings.ToLower(id)
		maxMode = true
	}
	// Cursor-hosted Grok models currently require Max Mode on the Agent API.
	if strings.HasPrefix(lower, "cursor-grok-") || strings.HasPrefix(lower, "grok-") {
		maxMode = true
	}
	return id, maxMode
}

// --- Helpers ---

func cursorAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return v
	}
	return ""
}

func cursorRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["refresh_token"].(string); ok {
		return v
	}
	return ""
}

func applyCursorHeaders(req *http.Request, accessToken string) {
	for k, v := range cursorRequestHeaders(accessToken, "") {
		if strings.HasPrefix(k, ":") {
			continue
		}
		// HTTP headers are canonicalized; keep standard names.
		switch strings.ToLower(k) {
		case "authorization":
			req.Header.Set("Authorization", v)
		case "content-type":
			req.Header.Set("Content-Type", v)
		case "connect-protocol-version":
			req.Header.Set("Connect-Protocol-Version", v)
		case "te":
			req.Header.Set("Te", v)
		default:
			// Preserve x-cursor-* casing style used by IDE.
			req.Header.Set(k, v)
		}
	}
}

// extractCCH extracts the cch value from the system prompt's billing header.
func extractCCH(systemPrompt string) string {
	idx := strings.Index(systemPrompt, "cch=")
	if idx < 0 {
		return ""
	}
	rest := systemPrompt[idx+4:]
	end := strings.IndexAny(rest, "; \n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func cursorSourceSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, metadata := range []map[string]any{opts.Metadata, req.Metadata} {
		if metadata == nil {
			continue
		}
		if value, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
			if normalized := cliproxysession.NormalizeExplicitID(value); normalized != "" {
				return normalized
			}
		}
	}

	for _, name := range []string{
		"X-Claude-Code-Session-Id",
		"X-Session-ID",
		"Session-Id",
		"Session_id",
		"X-Session-Affinity",
	} {
		if normalized := cliproxysession.NormalizeExplicitID(opts.Headers.Get(name)); normalized != "" {
			return normalized
		}
	}

	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if normalized := cliproxysession.ClaudeMetadataSessionID(payload); normalized != "" {
			return normalized
		}
		for _, path := range []string{"session_id", "sessionId", "conversation_id", "prompt_cache_key"} {
			if normalized := cliproxysession.NormalizeExplicitID(gjson.GetBytes(payload, path).String()); normalized != "" {
				return normalized
			}
		}
	}

	if derived := cliproxysession.DerivedID(opts.Metadata); derived != "" {
		return derived
	}
	return cliproxysession.DerivedID(req.Metadata)
}

func scopeCursorSourceSessionID(sourceSessionID string, parsed *parsedOpenAIRequest) string {
	if sourceSessionID == "" || parsed == nil {
		return sourceSessionID
	}
	// A Claude Code process may host concurrent subagents. Scope the explicit
	// client session by the stable conversation root so their checkpoints cannot
	// overwrite each other, while tool-call lineage preserves later continuations.
	root := deriveSessionKey("", parsed.Model, parsed.Messages)
	if root == "" {
		return sourceSessionID
	}
	return sourceSessionID + ":" + root
}

// deriveConversationId generates a conversation_id for Cursor AgentService.
// Priority:
//  1. Claude Code / client session_id — stable so checkpoints and tool resume work
//  2. Fresh UUID — OpenAI-style requests must not reuse a server conversation without
//     checkpoint blobs (same system prompt previously caused multi-turn "missing blob")
func deriveConversationId(apiKey, sessionId, systemPrompt string) string {
	_ = systemPrompt // retained for call-site compatibility; no longer used without session
	if sessionId != "" {
		input := "cursor-conv:" + apiKey + ":" + sessionId
		h := sha256.Sum256([]byte(input))
		s := hex.EncodeToString(h[:16])
		return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:32])
	}
	// New conversation per request for stateless OpenAI chat completions.
	return uuid.New().String()
}

func deriveSessionKey(clientKey string, model string, messages []gjson.Result) string {
	var firstUserContent string
	var systemContent string
	for _, msg := range messages {
		role := msg.Get("role").String()
		if role == "user" && firstUserContent == "" {
			firstUserContent = extractTextContent(msg.Get("content"))
		} else if role == "system" && systemContent == "" {
			// System prompt differs per Claude Code session (contains cwd, tools, etc.).
			systemContent = extractTextContent(msg.Get("content"))
		}
	}
	// Include client API key + system prompt hash to prevent session collisions:
	// - Different users have different API keys
	// - Different Claude Code sessions have different system prompts (cwd, tools, etc.)
	input := clientKey + ":" + model + ":" + systemContent + ":" + firstUserContent
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func sseChunk(id string, created int64, model string, delta string, finishReason string) cliproxyexecutor.StreamChunk {
	fr := "null"
	if finishReason != "" {
		fr = finishReason
	}
	// Note: the framework's WriteChunk adds "data: " prefix and "\n\n" suffix,
	// so we only output the raw JSON here.
	data := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
		id, created, model, delta, fr)
	return cliproxyexecutor.StreamChunk{
		Payload: []byte(data),
	}
}

// buildCursorOpenAIChatCompletion builds a non-stream OpenAI chat.completion
// payload with content and reasoning_content split (Codex-compatible).
func buildCursorOpenAIChatCompletion(id string, created int64, model, content, reasoning string, promptTokens, completionTokens int64) []byte {
	type message struct {
		Role             string `json:"role"`
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content,omitempty"`
	}
	type choice struct {
		Index        int     `json:"index"`
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}
	type usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	}
	type resp struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Created int64    `json:"created"`
		Model   string   `json:"model"`
		Choices []choice `json:"choices"`
		Usage   usage    `json:"usage"`
	}
	out := resp{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []choice{{
			Index: 0,
			Message: message{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: reasoning,
			},
			FinishReason: "stop",
		}},
		Usage: usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Fallback should never happen for this shape; keep empty content response.
		return []byte(`{"id":"","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	}
	return b
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeMcpArgsToJSON(args map[string][]byte) string {
	if len(args) == 0 {
		return "{}"
	}
	result := make(map[string]interface{})
	for k, v := range args {
		// Try protobuf Value decoding first (matches TS: toJson(ValueSchema, fromBinary(ValueSchema, value)))
		if decoded, err := cursorproto.ProtobufValueBytesToJSON(v); err == nil {
			result[k] = decoded
		} else {
			// Fallback: try raw JSON
			var jsonVal interface{}
			if err := json.Unmarshal(v, &jsonVal); err == nil {
				result[k] = jsonVal
			} else {
				result[k] = string(v)
			}
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// --- Model Discovery ---

// cursorModelsCache stores the last successful model list for each auth ID.
// A transient models request failure must not replace a verified live catalog
// with the hardcoded cold-start fallback.
var (
	cursorModelsCacheMu sync.RWMutex
	cursorModelsCache   = make(map[string][]*registry.ModelInfo)
)

// cursorModelsOrFallback returns an independent copy of the last successful
// list for authID. The hardcoded fallback is used only before that auth has a
// successful live fetch.
func cursorModelsOrFallback(authID string) []*registry.ModelInfo {
	if authID != "" {
		cursorModelsCacheMu.RLock()
		cached, ok := cursorModelsCache[authID]
		if ok && len(cached) > 0 {
			models := cloneCursorModelInfos(cached)
			cursorModelsCacheMu.RUnlock()
			return models
		}
		cursorModelsCacheMu.RUnlock()
	}
	return GetCursorFallbackModels()
}

// cacheCursorModels records a complete, independent snapshot after a
// successful live fetch. Each success replaces the previous snapshot for the
// same auth ID.
func cacheCursorModels(authID string, models []*registry.ModelInfo) {
	if authID == "" || len(models) == 0 {
		return
	}

	snapshot := cloneCursorModelInfos(models)
	cursorModelsCacheMu.Lock()
	cursorModelsCache[authID] = snapshot
	cursorModelsCacheMu.Unlock()
}

func cloneCursorModelInfos(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}

	cloned := make([]*registry.ModelInfo, len(models))
	for i, model := range models {
		cloned[i] = cloneCursorModelInfo(model)
	}
	return cloned
}

func cloneCursorModelInfo(model *registry.ModelInfo) *registry.ModelInfo {
	if model == nil {
		return nil
	}

	cloned := *model
	cloned.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	cloned.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	cloned.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	cloned.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	cloned.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Levels = append([]string(nil), model.Thinking.Levels...)
		cloned.Thinking = &thinking
	}
	if model.Config != nil {
		modelConfig := *model.Config
		if model.Config.OverrideHeader != nil {
			modelConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				modelConfig.OverrideHeader[key] = value
			}
		}
		cloned.Config = &modelConfig
	}
	return &cloned
}

// FetchCursorModels retrieves available models from Cursor's API.
func FetchCursorModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if auth == nil {
		return GetCursorFallbackModels()
	}

	authID := auth.ID
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return cursorModelsOrFallback(authID)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	httpClient, errClient := cursorauth.NewHTTPClient(cursorProxyURL(cfg, auth), 0)
	if errClient != nil {
		log.Debugf("cursor: failed to configure models proxy: %v", errClient)
		return cursorModelsOrFallback(authID)
	}
	return fetchCursorModels(ctx, authID, accessToken, httpClient, cursorAPIURL+cursorModelsPath)
}

func fetchCursorModels(ctx context.Context, authID, accessToken string, client *http.Client, modelsURL string) []*registry.ModelInfo {
	// GetUsableModels is a unary RPC call (not streaming)
	// Send an empty protobuf request
	emptyReq := make([]byte, 0)

	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		modelsURL, bytes.NewReader(emptyReq))
	if err != nil {
		log.Debugf("cursor: failed to create models request: %v", err)
		return cursorModelsOrFallback(authID)
	}

	// GetUsableModels is unary proto (not connect+proto framing).
	applyCursorHeaders(h2Req, accessToken)
	h2Req.Header.Set("Content-Type", "application/proto")

	resp, err := client.Do(h2Req)
	if err != nil {
		log.Debugf("cursor: models request failed: %v", err)
		return cursorModelsOrFallback(authID)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("cursor: models request returned status %d", resp.StatusCode)
		return cursorModelsOrFallback(authID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cursorModelsOrFallback(authID)
	}

	models := parseModelsResponse(body)
	if len(models) == 0 {
		return cursorModelsOrFallback(authID)
	}
	cacheCursorModels(authID, models)
	return models
}

func parseModelsResponse(data []byte) []*registry.ModelInfo {
	// Try stripping Connect framing first
	if len(data) >= cursorproto.ConnectFrameHeaderSize {
		_, payload, _, ok := cursorproto.ParseConnectFrame(data)
		if ok {
			data = payload
		}
	}

	// The response is a GetUsableModelsResponse protobuf.
	// We need to decode it manually - it contains a repeated "models" field.
	// Based on the TS code, the response has a `models` field (repeated) containing
	// model objects with modelId, displayName, thinkingDetails, etc.

	// For now, we'll try a simple decode approach
	var models []*registry.ModelInfo
	// Field 1 is likely "models" (repeated submessage)
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		if typ == 2 { // BytesType (submessage)
			val, n := consumeBytes(data)
			if n < 0 {
				break
			}
			data = data[n:]

			if num == 1 { // models field
				if m := parseModelEntry(val); m != nil {
					models = append(models, m)
				}
			}
		} else {
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
		}
	}

	return models
}

func parseModelEntry(data []byte) *registry.ModelInfo {
	var modelId, displayName string
	var hasThinking bool

	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		switch typ {
		case 2: // BytesType
			val, n := consumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 1: // modelId
				modelId = string(val)
			case 2: // thinkingDetails
				hasThinking = true
			case 3: // displayModelId (use as fallback)
				if displayName == "" {
					displayName = string(val)
				}
			case 4: // displayName
				displayName = string(val)
			case 5: // displayNameShort
				if displayName == "" {
					displayName = string(val)
				}
			}
		case 0: // VarintType
			_, n := consumeVarint(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		default:
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}

	if modelId == "" {
		return nil
	}
	if displayName == "" {
		displayName = modelId
	}

	info := &registry.ModelInfo{
		ID:                  modelId,
		Object:              "model",
		Created:             time.Now().Unix(),
		OwnedBy:             "cursor",
		Type:                cursorAuthType,
		DisplayName:         displayName,
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
	}
	if hasThinking {
		info.Thinking = &registry.ThinkingSupport{
			Max:            50000,
			DynamicAllowed: true,
		}
	}
	return info
}

// GetCursorFallbackModels returns hardcoded fallback models.
func GetCursorFallbackModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
}

// Low-level protowire helpers (avoid importing protowire in executor)
func consumeTag(b []byte) (num int, typ int, n int) {
	v, n := consumeVarint(b)
	if n < 0 {
		return 0, 0, -1
	}
	return int(v >> 3), int(v & 7), n
}

func consumeVarint(b []byte) (uint64, int) {
	var val uint64
	for i := 0; i < len(b) && i < 10; i++ {
		val |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return val, i + 1
		}
	}
	return 0, -1
}

func consumeBytes(b []byte) ([]byte, int) {
	length, n := consumeVarint(b)
	if n < 0 || int(length) > len(b)-n {
		return nil, -1
	}
	return b[n : n+int(length)], n + int(length)
}

func consumeFieldValue(num, typ int, b []byte) int {
	switch typ {
	case 0: // Varint
		_, n := consumeVarint(b)
		return n
	case 1: // 64-bit
		if len(b) < 8 {
			return -1
		}
		return 8
	case 2: // Length-delimited
		_, n := consumeBytes(b)
		return n
	case 5: // 32-bit
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}
