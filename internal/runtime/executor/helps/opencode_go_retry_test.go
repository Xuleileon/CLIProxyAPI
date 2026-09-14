package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type handshakeTimeoutForTest struct{}

func (handshakeTimeoutForTest) Error() string   { return "net/http: TLS handshake timeout" }
func (handshakeTimeoutForTest) Timeout() bool   { return true }
func (handshakeTimeoutForTest) Temporary() bool { return true }

func TestOpenCodeTimeoutReplayBoundary(t *testing.T) {
	for _, safe := range []bool{false, true} {
		err := openCodeGoTracedConnectionError(&url.Error{Op: "Post", URL: "https://example.invalid", Err: handshakeTimeoutForTest{}}, safe)
		wrapped, ok := err.(openCodeGoConnectionError)
		if !ok || !wrapped.IsConnectionLifecycle() {
			t.Fatalf("missing transport classification: %T", err)
		}
		if (wrapped.RetryAfter() != nil) != safe || wrapped.IsRequestScoped() == safe {
			t.Fatalf("incorrect replay boundary: safe=%v", safe)
		}
	}
}

func TestOpenCodeGoRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		status  int
		body    string
		want    time.Duration
	}{
		{"rate limit", nil, 429, `{"error":"rate_limit_exceeded"}`, -1},
		{"busy", nil, 503, "", 2 * time.Second},
		{"milliseconds", http.Header{"Retry-After-Ms": {"250"}}, 429, "", 250 * time.Millisecond},
		{"seconds", http.Header{"Retry-After": {"120"}}, 429, "", 120 * time.Second},
		{"quota", nil, 429, "GoUsageLimitError", -1},
		{"invalid request", nil, 400, "", -1},
		{"bad header", http.Header{"Retry-After": {"NaN"}}, 429, "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OpenCodeGoRetryAfter(tc.headers, tc.status, []byte(tc.body))
			if tc.want < 0 {
				if got != nil {
					t.Fatalf("unexpected retry: %v", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("retry=%v, want %v", got, tc.want)
			}
		})
	}
	d := OpenCodeGoRetryAfter(http.Header{"Retry-After": {time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)}}, 429, nil)
	if d == nil || *d < 58*time.Second || *d > time.Minute {
		t.Fatalf("HTTP date delay=%v", d)
	}
}

func TestOpenCodeGoConnectionError(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://example.test", Err: io.EOF}
	got := openCodeGoTracedConnectionError(err, true)
	var retry interface {
		StatusCode() int
		RetryAfter() *time.Duration
	}
	if !errors.As(got, &retry) || retry.StatusCode() != 502 || *retry.RetryAfter() != 250*time.Millisecond || !errors.Is(got, io.EOF) {
		t.Fatalf("lost retry or cause: %v", got)
	}
	for _, stop := range []error{context.Canceled, context.DeadlineExceeded, errors.New("bad certificate")} {
		if openCodeGoTracedConnectionError(stop, true) != stop {
			t.Fatalf("unexpected retry for %v", stop)
		}
	}
}
