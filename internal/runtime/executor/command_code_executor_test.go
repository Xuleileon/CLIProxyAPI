package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCommandCodeExecutorProtocols(t *testing.T) {
	for _, format := range []tr.Format{tr.FormatOpenAI, tr.FormatClaude, tr.FormatOpenAIResponse} {
		for _, stream := range []bool{false, true} {
			t.Run(format.String()+map[bool]string{true: "-stream", false: "-json"}[stream], func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != "/alpha/generate" || r.Header.Get("Authorization") != "Bearer test" || gjson.GetBytes(body, "params.model").String() != "m" {
						t.Error("wrong upstream request")
					}
					if !strings.Contains(string(body), "hello") {
						t.Errorf("lost message %s", body)
					}
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, "{\"type\":\"text-delta\",\"text\":\"ANSWER\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\",\"totalUsage\":{\"inputTokens\":10,\"outputTokens\":2}}\n")
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
				}))
				defer server.Close()
				payload := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
				if format == tr.FormatOpenAIResponse {
					payload = `{"model":"m","input":"hello","max_output_tokens":64}`
				}
				if stream {
					payload = strings.TrimSuffix(payload, "}") + `,"stream":true}`
				}
				a := &auth.Auth{ID: "test", Provider: "command-code", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
				e := NewCommandCodeExecutor(&config.Config{})
				r := ex.Request{Model: "m", Payload: []byte(payload)}
				o := ex.Options{SourceFormat: format, ResponseFormat: format, Stream: stream, OriginalRequest: []byte(payload)}
				start := time.Now()
				var output strings.Builder
				if stream {
					result, err := e.ExecuteStream(context.Background(), a, r, o)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output.Write(chunk.Payload)
					}
				} else {
					result, err := e.Execute(context.Background(), a, r, o)
					if err != nil {
						t.Fatal(err)
					}
					output.Write(result.Payload)
				}
				if !strings.Contains(output.String(), "ANSWER") {
					t.Fatalf("lost output: %s", output.String())
				}
				if time.Since(start) > time.Second {
					t.Fatal("waited for EOF after finish")
				}
			})
		}
	}
}

func TestCommandCodeExecutorDoesNotAcceptHTTP200ErrorOrEOF(t *testing.T) {
	for _, events := range []string{`{"type":"start"}` + "\n", `{"type":"error","error":{"statusCode":503,"message":"unavailable"}}` + "\n"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, events) }))
		a := &auth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
		e := NewCommandCodeExecutor(&config.Config{})
		r := ex.Request{Model: "m", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
		o := ex.Options{SourceFormat: tr.FormatOpenAI}
		if _, err := e.Execute(context.Background(), a, r, o); err == nil {
			t.Error("false JSON success")
		}
		result, err := e.ExecuteStream(context.Background(), a, r, o)
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		for c := range result.Chunks {
			if c.Err != nil {
				failed = true
			}
			if strings.Contains(string(c.Payload), "[DONE]") {
				t.Error("false stream success")
			}
		}
		if !failed {
			t.Error("missing stream error")
		}
		server.Close()
	}
}
