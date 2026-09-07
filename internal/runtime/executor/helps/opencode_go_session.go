package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// OpenCodeGoUserAgent identifies the subscription adapter without exposing the
// host application, its build, or machine information.
const OpenCodeGoUserAgent = "GoSubscriptionClient/1.0"

// ApplyOpenCodeGoSessionHeader preserves configured session headers, then client
// headers, and otherwise maps the existing conversation identity to an opaque ID.
// Call after applying custom headers and before logging or sending the request.
func ApplyOpenCodeGoSessionHeader(out *http.Request, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) {
	const header = "X-Opencode-Session"
	if id := cliproxysession.NormalizeExplicitID(out.Header.Get(header)); id != "" {
		out.Header.Set(header, id)
		return
	}
	for name, values := range opts.Headers {
		if strings.EqualFold(name, header) {
			for _, value := range values {
				if id := cliproxysession.NormalizeExplicitID(value); id != "" {
					out.Header.Set(header, id)
					return
				}
			}
		}
	}
	// Enrich also handles direct SDK executions which bypass the auth manager.
	req, opts = cliproxysession.Enrich(req, opts)
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	identity := cliproxyauth.ExtractSessionID(opts.Headers, payload, opts.Metadata)
	if identity == "" {
		// An identity-free request cannot be correlated with future turns. Keep
		// identical retries stable without grouping all traffic under one ID.
		sum := sha256.Sum256(payload)
		identity = "request:" + hex.EncodeToString(sum[:])
	}
	scope := metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
	if scope == "" {
		scope = metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
	}
	out.Header.Set(header, stableProviderSessionUUID("opencode-go", "conversation", scope+"\x00"+identity))
}

// ApplyOpenCodeGoHTTPRequestSession preserves the body while identifying raw
// management requests. Inference requests normally use the executor options.
func ApplyOpenCodeGoHTTPRequestSession(out *http.Request) error {
	var payload []byte
	if out.Body != nil {
		var err error
		payload, err = io.ReadAll(out.Body)
		errClose := out.Body.Close()
		if err != nil {
			return fmt.Errorf("read OpenCode session payload: %w", err)
		}
		if errClose != nil {
			return fmt.Errorf("close OpenCode session payload: %w", errClose)
		}
		out.Body = io.NopCloser(bytes.NewReader(payload))
	}
	format := sdktranslator.FormatOpenAI
	if strings.HasSuffix(out.URL.Path, "/responses") {
		format = sdktranslator.FormatOpenAIResponse
	} else if strings.HasSuffix(out.URL.Path, "/messages") {
		format = sdktranslator.FormatClaude
	}
	ApplyOpenCodeGoSessionHeader(out, cliproxyexecutor.Request{Payload: payload}, cliproxyexecutor.Options{Headers: out.Header, SourceFormat: format})
	return nil
}
