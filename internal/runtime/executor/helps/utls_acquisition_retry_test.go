package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestUtlsAcquisitionEOFRetriesWithoutReadingRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"EOF", io.EOF, 3},
		{"truncated handshake", io.ErrUnexpectedEOF, 3},
		{"certificate error", errors.New("certificate verification failed"), 1},
		{"canceled", context.Canceled, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			rt := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
				attempts++
				return nil, tc.err
			})}
			body := strings.NewReader("inference payload must not be replayed")
			size := body.Len()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.test/responses", body)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rt.RoundTrip(req)
			if !errors.Is(err, tc.err) || attempts != tc.attempts || body.Len() != size {
				t.Fatalf("attempts=%d unread=%d error=%v", attempts, body.Len(), err)
			}
		})
	}
}
