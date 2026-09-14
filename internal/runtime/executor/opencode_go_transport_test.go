package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type openCodeGoFaultTransport struct {
	failure    error
	attempts   int
	sent       bool
	alwaysFail bool
	sessions   []string
	bodies     []string
}

func (tr *openCodeGoFaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.attempts++
	tr.sessions = append(tr.sessions, req.Header.Get("X-Opencode-Session"))
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	tr.bodies = append(tr.bodies, string(body))
	trace := httptrace.ContextClientTrace(req.Context())
	trace.GetConn(req.URL.Host)
	if tr.sent {
		trace.GotConn(httptrace.GotConnInfo{})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	if tr.attempts == 1 || tr.alwaysFail {
		if tr.failure != nil {
			return nil, tr.failure
		}
		return nil, io.EOF
	}
	return &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")),
	}, nil
}

type openCodeHandshakeTimeout struct{}

func (openCodeHandshakeTimeout) Error() string   { return "net/http: TLS handshake timeout" }
func (openCodeHandshakeTimeout) Timeout() bool   { return true }
func (openCodeHandshakeTimeout) Temporary() bool { return true }

func TestOpenCodeGoTransportRetryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                string
		timeout             bool
		sent, alwaysFail    bool
		retry, wantAttempts int
	}{
		{"unsent reconnect", false, false, false, 1, 2},
		{"submitted request stops", false, true, false, 3, 1},
		{"total budget", false, false, true, 1, 2},
		{"disabled retries", false, false, false, 0, 1},
		{"unsent reconnect", true, false, false, 1, 2},
		{"timeout budget", true, false, true, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &openCodeGoFaultTransport{sent: tc.sent, alwaysFail: tc.alwaysFail}
			if tc.timeout {
				tr.failure = openCodeHandshakeTimeout{}
			}
			writer := httptest.NewRecorder()
			gc, _ := gin.CreateTestContext(writer)
			ctx := context.WithValue(context.Background(), "gin", gc)
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", tr)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(tc.retry, 5*time.Second, 0)
			manager.RegisterExecutor(NewOpenCodeGoExecutor(&config.Config{}))
			auth := openCodeGoTestAuth("https://upstream.test/v1")
			auth.ID = "transport-" + tc.name
			model := "muse-spark-1.3-contributor"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "opencode-go", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			result, err := manager.ExecuteStream(ctx, []string{"opencode-go"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"muse-spark-1.3-contributor","input":"preserve this complete request"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if result != nil {
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						err = chunk.Err
					}
				}
			}
			if tc.name == "unsent reconnect" && err != nil {
				t.Fatal(err)
			}
			if tc.name != "unsent reconnect" && err == nil {
				t.Fatal("expected upstream failure")
			}
			if tr.attempts != tc.wantAttempts {
				t.Fatalf("attempts=%d, want %d", tr.attempts, tc.wantAttempts)
			}
			updated, _ := manager.GetByID(auth.ID)
			if state := updated.ModelStates[model]; state != nil && state.Unavailable {
				t.Fatal("transport failure cooled healthy credential")
			}
			if tc.sent && !errors.Is(err, io.EOF) {
				t.Fatalf("lost error cause: %v", err)
			}
			if tr.attempts == 2 && time.Since(start) >= 1500*time.Millisecond {
				t.Fatalf("reconnect took %v, expected subsecond backoff", time.Since(start))
			}
			if writer.Header().Get("X-CPA-Retry-Handled") != "true" {
				t.Fatal("missing client retry ownership header")
			}
			for i := 1; i < len(tr.sessions); i++ {
				if tr.sessions[i] == "" || tr.sessions[i] != tr.sessions[0] || tr.bodies[i] != tr.bodies[0] {
					t.Fatal("retry changed session or payload")
				}
			}
		})
	}
}
