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

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// CommandCodeExecutor translates the Go CLI bridge into CPA's native client protocols.
type CommandCodeExecutor struct{ cfg *config.Config }

func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{cfg: cfg}
}
func (e *CommandCodeExecutor) Identifier() string { return "command-code" }
func (e *CommandCodeExecutor) RequestToFormat(_ ex.Request, _ ex.Options) tr.Format {
	return tr.FormatOpenAI
}
func (e *CommandCodeExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}
func (e *CommandCodeExecutor) CountTokens(ctx context.Context, a *auth.Auth, r ex.Request, o ex.Options) (ex.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, a, r, o)
}

func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, a *auth.Auth, r *http.Request) (*http.Response, error) {
	if r == nil {
		return nil, fmt.Errorf("command-code: nil request")
	}
	r = r.Clone(ctx)
	e.headers(r, a)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0).Do(r)
}
func (e *CommandCodeExecutor) headers(r *http.Request, a *auth.Auth) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "CLIProxyAPI/command-code")
	r.Header.Set("x-command-code-version", "1.53.0")
	r.Header.Set("x-cli-environment", "production")
	if a != nil {
		r.Header.Set("Authorization", "Bearer "+a.Attributes["api_key"])
		util.ApplyCustomHeadersFromAttrs(r, a.Attributes)
	}
}

func (e *CommandCodeExecutor) prepare(ctx context.Context, a *auth.Auth, r ex.Request, o ex.Options) (*http.Response, []byte, error) {
	if o.Alt != "" {
		return nil, nil, statusErr{code: 400, msg: "command-code does not support this alternate endpoint"}
	}
	model := thinking.ParseSuffix(r.Model).ModelName
	translated := tr.TranslateRequest(o.SourceFormat, tr.FormatOpenAI, model, r.Payload, o.Stream)
	var err error
	translated, err = helps.ApplyRequestThinking(translated, r, o, o.SourceFormat.String(), "openai", e.Identifier())
	if err != nil {
		return nil, nil, err
	}
	session := ""
	for _, header := range []string{"x-session-id", "x-opencode-session", "session_id"} {
		if session = o.Headers.Get(header); session != "" {
			break
		}
	}
	if session == "" && o.Metadata != nil {
		session, _ = o.Metadata[ex.ExecutionSessionMetadataKey].(string)
	}
	if session == "" {
		session = uuid.NewString()
	} else {
		identity := ""
		if a != nil {
			identity = a.ID
		}
		session = uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity+"\x00"+session)).String()
	}
	body, err := helps.CommandCodeRequest(translated, model, session)
	if err != nil {
		return nil, nil, statusErr{code: 400, msg: err.Error()}
	}
	base := config.DefaultCommandCodeBaseURL
	if a != nil && a.Attributes["base_url"] != "" {
		base = a.Attributes["base_url"]
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/alpha/generate", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	e.headers(request, a)
	request.Header.Set("x-session-id", session)
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0).Do(request)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Debugf("command-code close: %v", errClose)
			}
		}()
		b, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, nil, readErr
		}
		msg := string(b)
		if a != nil {
			msg = strings.ReplaceAll(msg, a.Attributes["api_key"], "[REDACTED]")
		}
		return nil, nil, statusErr{code: resp.StatusCode, msg: msg}
	}
	return resp, translated, nil
}

func (e *CommandCodeExecutor) Execute(ctx context.Context, a *auth.Auth, r ex.Request, o ex.Options) (result ex.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, r.Model, a)
	defer reporter.TrackFailure(ctx, &err)
	resp, translated, err := e.prepare(ctx, a, r, o)
	if err != nil {
		return result, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("command-code close: %v", errClose)
		}
	}()
	stream := helps.NewCommandCodeStream(r.Model)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if _, err = stream.Consume(scanner.Bytes()); err != nil {
			return result, err
		}
		if stream.Finished {
			break
		}
	}
	if err = scanner.Err(); err != nil {
		return result, err
	}
	if !stream.Finished {
		return result, io.ErrUnexpectedEOF
	}
	body := stream.Response()
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	var param any
	out := tr.TranslateNonStream(ctx, tr.FormatOpenAI, ex.ResponseFormatOrSource(o), r.Model, o.OriginalRequest, translated, body, &param)
	return ex.Response{Payload: out, Headers: resp.Header.Clone()}, nil
}

func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, r ex.Request, o ex.Options) (_ *ex.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, r.Model, a)
	defer reporter.TrackFailure(ctx, &err)
	resp, translated, err := e.prepare(ctx, a, r, o)
	if err != nil {
		return nil, err
	}
	ch := make(chan ex.StreamChunk)
	go func() {
		defer close(ch)
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Debugf("command-code close: %v", errClose)
			}
		}()
		send := func(c ex.StreamChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		fail := func(err error) { reporter.PublishFailure(ctx, err); send(ex.StreamChunk{Err: err}) }
		state := helps.NewCommandCodeStream(r.Model)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		var param any
		emit := func(b []byte) bool {
			for _, chunk := range tr.TranslateStream(ctx, tr.FormatOpenAI, ex.ResponseFormatOrSource(o), r.Model, o.OriginalRequest, translated, b, &param) {
				if !send(ex.StreamChunk{Payload: chunk}) {
					return false
				}
			}
			return true
		}
		for scanner.Scan() {
			chunks, errConsume := state.Consume(scanner.Bytes())
			if errConsume != nil {
				fail(errConsume)
				return
			}
			for _, b := range chunks {
				if !emit(b) {
					reporter.PublishFailure(ctx, ctx.Err())
					return
				}
			}
			if state.Finished {
				usage, _ := json.Marshal(map[string]any{"usage": state.Usage})
				reporter.Publish(ctx, helps.ParseOpenAIUsage(usage))
				emit([]byte("data: [DONE]\n\n"))
				return
			}
		}
		errScan := scanner.Err()
		if errScan == nil {
			errScan = io.ErrUnexpectedEOF
		}
		fail(errScan)
	}()
	return &ex.StreamResult{Headers: resp.Header.Clone(), Chunks: ch}, nil
}
