package helps

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

// OpenCodeGoRetryAfter preserves the provider's delay and supplies the native
// client's short fallback for transient failures, not subscription exhaustion.
func OpenCodeGoRetryAfter(headers http.Header, status int, body []byte) *time.Duration {
	for _, field := range []struct {
		name string
		unit time.Duration
	}{{"Retry-After-Ms", time.Millisecond}, {"Retry-After", time.Second}} {
		raw := strings.TrimSpace(headers.Get(field.name))
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) && n < float64(math.MaxInt64)/float64(field.unit) {
			d := time.Duration(n * float64(field.unit))
			return &d
		}
		if field.name == "Retry-After" {
			if at, err := http.ParseTime(raw); err == nil {
				d := max(time.Until(at), 0)
				return &d
			}
		}
	}
	if strings.Contains(string(body), "GoUsageLimitError") || strings.Contains(string(body), "FreeUsageLimitError") {
		return nil
	}
	// The manager owns shared, increasing backoff for headerless rate limits.
	if status == 500 || status == 502 || status == 503 || status == 504 {
		d := 2 * time.Second
		return &d
	}
	return nil
}

type openCodeGoConnectionError struct {
	error
	safeToRetry bool
}

func (e openCodeGoConnectionError) Unwrap() error         { return e.error }
func (e openCodeGoConnectionError) StatusCode() int       { return http.StatusBadGateway }
func (e openCodeGoConnectionError) IsRequestScoped() bool { return !e.safeToRetry }
func (e openCodeGoConnectionError) RetryAfter() *time.Duration {
	if !e.safeToRetry {
		return nil
	}
	d := 250 * time.Millisecond
	return &d
}

// Only traced connection acquisition failures are safe to replay.
// A failure after GotConn can have already submitted inference work.
func openCodeGoTracedConnectionError(err error, safeToRetry bool) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var op *net.OpError
	var streamErr http2.StreamError
	var goAwayErr http2.GoAwayError
	var connectionErr http2.ConnectionError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &op) ||
		errors.As(err, &streamErr) || errors.As(err, &goAwayErr) || errors.As(err, &connectionErr) {
		return openCodeGoConnectionError{error: err, safeToRetry: safeToRetry}
	}
	return err
}
