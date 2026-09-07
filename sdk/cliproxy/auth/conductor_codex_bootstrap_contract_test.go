package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexBufferedRequestErrorRemainsInStream(t *testing.T) {
	m := NewManager(nil, nil, nil)
	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	m.SetConfig(cfg)
	m.SetRetryConfig(0, 0, 2)
	registerOverloadAuths(t, m, 2)
	calls := 0
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		calls++
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\"}\n\n")}
		ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusBadRequest, msg: "invalid input"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}})
	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("request error escaped the committed stream: %v", err)
	}
	var frames, failures int
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			failures++
		} else {
			frames++
		}
	}
	if calls != 1 || frames != 1 || failures != 1 {
		t.Fatalf("calls=%d frames=%d errors=%d", calls, frames, failures)
	}
}
