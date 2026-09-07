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
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		d := 2 * time.Second
		return &d
	}
	return nil
}

type openCodeGoConnectionError struct{ error }

func (e openCodeGoConnectionError) Unwrap() error   { return e.error }
func (e openCodeGoConnectionError) StatusCode() int { return http.StatusBadGateway }
func (e openCodeGoConnectionError) RetryAfter() *time.Duration {
	d := 2 * time.Second
	return &d
}

// OpenCodeGoConnectionError is only used before an HTTP response is available.
// Once output is streaming, the manager must not replay delivered content.
func OpenCodeGoConnectionError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var op *net.OpError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &op) {
		return openCodeGoConnectionError{err}
	}
	return err
}
