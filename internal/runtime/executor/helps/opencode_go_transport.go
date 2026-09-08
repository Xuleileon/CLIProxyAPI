package helps

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DoOpenCodeGoRequest leaves retries to the manager's single request budget.
// Only a failed connection acquisition can be replayed without ambiguity.
func DoOpenCodeGoRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		ginCtx.Header("X-CPA-Retry-Handled", "true")
	}
	trace := &openCodeGoHTTPTrace{started: time.Now()}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace.hooks()))
	resp, err := client.Do(req)
	trace.mu.Lock()
	safeToRetry := trace.acquiring && !trace.connected
	fields := log.Fields{
		"elapsed_ms": time.Since(trace.started).Milliseconds(),
		"connected":  trace.connected, "reused": trace.reused,
		"tls_done_ms": trace.tls.Milliseconds(), "request_written_ms": trace.written.Milliseconds(),
		"first_byte_ms": trace.firstByte.Milliseconds(), "request_bytes": req.ContentLength,
	}
	trace.mu.Unlock()
	if resp != nil {
		fields["status"] = resp.StatusCode
		fields["http"] = resp.Proto
	}
	if err != nil {
		err = openCodeGoTracedConnectionError(err, safeToRetry)
		fields["retry_safe"] = safeToRetry
		// Keep URLs, credentials, and arbitrary transport error text out of logs.
		LogWithRequestID(ctx).WithFields(fields).Warnf("opencode-go: transport failed connected=%t reused=%t retry_safe=%t elapsed_ms=%d tls_done_ms=%d request_written_ms=%d first_byte_ms=%d request_bytes=%d",
			fields["connected"], fields["reused"], safeToRetry, fields["elapsed_ms"], fields["tls_done_ms"], fields["request_written_ms"], fields["first_byte_ms"], req.ContentLength)
	} else {
		LogWithRequestID(ctx).WithFields(fields).Debugf("opencode-go: response headers status=%d http=%s connected=%t reused=%t elapsed_ms=%d tls_done_ms=%d request_written_ms=%d first_byte_ms=%d request_bytes=%d",
			resp.StatusCode, resp.Proto, fields["connected"], fields["reused"], fields["elapsed_ms"], fields["tls_done_ms"], fields["request_written_ms"], fields["first_byte_ms"], req.ContentLength)
	}
	return resp, err
}

type openCodeGoHTTPTrace struct {
	mu                           sync.Mutex
	started                      time.Time
	acquiring, connected, reused bool
	tls, written, firstByte      time.Duration
}

func (t *openCodeGoHTTPTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			t.mu.Lock()
			t.acquiring = true
			t.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			t.connected = true
			t.reused = info.Reused
			t.mu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			t.mu.Lock()
			t.tls = time.Since(t.started)
			t.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			t.mu.Lock()
			t.written = time.Since(t.started)
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			t.firstByte = time.Since(t.started)
			t.mu.Unlock()
		},
	}
}
