package proto

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type redirectContextDialer struct {
	address string
	dials   atomic.Int64
	failTLS atomic.Int64
}

func (d *redirectContextDialer) Dial(network, _ string) (net.Conn, error) {
	if d.shouldFailTLS() {
		return closedPipeConnection(), nil
	}
	return net.Dial(network, d.address)
}

func (d *redirectContextDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	if d.shouldFailTLS() {
		return closedPipeConnection(), nil
	}
	return (&net.Dialer{}).DialContext(ctx, network, d.address)
}

func (d *redirectContextDialer) shouldFailTLS() bool {
	attempt := d.dials.Add(1)
	return attempt <= d.failTLS.Load()
}

func closedPipeConnection() net.Conn {
	client, server := net.Pipe()
	_ = server.Close()
	return client
}

func TestH2StreamPoolMultiplexesAndIsolatesStreamClose(t *testing.T) {
	t.Parallel()

	server := newH2EchoServer(t)
	dialer := &redirectContextDialer{address: server.Listener.Addr().String()}
	pool := NewH2StreamPool()
	pool.tlsConfig = &tls.Config{InsecureSkipVerify: true} // Test server certificate.
	headers := map[string]string{
		":path":                    "/agent.v1.AgentService/Run",
		"content-type":             "application/connect+proto",
		"connect-protocol-version": "1",
		"authorization":            "Bearer test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := pool.Open(ctx, "account|direct", "api2.cursor.sh", headers, dialer)
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	defer first.Close()
	second, err := pool.Open(ctx, "account|direct", "api2.cursor.sh", headers, dialer)
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	defer second.Close()

	first.Close()
	if err := second.Write([]byte("second")); err != nil {
		t.Fatalf("write second stream after closing first: %v", err)
	}
	if got := readH2StreamText(t, second); got != "ack:second" {
		t.Fatalf("second response = %q, want ack:second", got)
	}
	if got := dialer.dials.Load(); got != 1 {
		t.Fatalf("TLS dial count = %d, want one multiplexed connection", got)
	}
}

func TestH2StreamPoolReconnectsAfterConnectionLoss(t *testing.T) {
	t.Parallel()

	server := newH2EchoServer(t)
	dialer := &redirectContextDialer{address: server.Listener.Addr().String()}
	pool := NewH2StreamPool()
	pool.tlsConfig = &tls.Config{InsecureSkipVerify: true} // Test server certificate.
	headers := map[string]string{":path": "/agent.v1.AgentService/Run", "content-type": "application/connect+proto"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := pool.Open(ctx, "account|direct", "api2.cursor.sh", headers, dialer)
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	if err := first.Write([]byte("first")); err != nil {
		t.Fatalf("write first stream: %v", err)
	}
	if got := readH2StreamText(t, first); got != "ack:first" {
		t.Fatalf("first response = %q, want ack:first", got)
	}
	first.Close()

	server.CloseClientConnections()
	deadline := time.Now().Add(2 * time.Second)
	reconnected := false
	var lastErr error
	for time.Now().Before(deadline) {
		second, openErr := pool.Open(ctx, "account|direct", "api2.cursor.sh", headers, dialer)
		if openErr != nil {
			lastErr = openErr
			continue
		}
		writeErr := second.Write([]byte("reconnected"))
		if writeErr == nil {
			var response string
			response, lastErr = readH2StreamTextResult(second)
			if lastErr == nil && response == "ack:reconnected" {
				reconnected = true
			} else if lastErr == nil {
				lastErr = fmt.Errorf("unexpected response %q", response)
			}
		} else {
			lastErr = writeErr
		}
		second.Close()
		if reconnected {
			break
		}
	}
	if !reconnected {
		t.Fatalf("stream did not reconnect: %v", lastErr)
	}
	if got := dialer.dials.Load(); got < 2 {
		t.Fatalf("TLS dial count = %d, want a new connection after connection loss", got)
	}
}

func TestH2StreamPoolRetriesTransientTLSHandshakeEOF(t *testing.T) {
	t.Parallel()

	server := newH2EchoServer(t)
	dialer := &redirectContextDialer{address: server.Listener.Addr().String()}
	dialer.failTLS.Store(2)
	pool := NewH2StreamPool()
	pool.tlsConfig = &tls.Config{InsecureSkipVerify: true} // Test server certificate.
	headers := map[string]string{":path": "/agent.v1.AgentService/Run", "content-type": "application/connect+proto"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := pool.Open(ctx, "account|direct", "api2.cursor.sh", headers, dialer)
	if err != nil {
		t.Fatalf("open stream after transient TLS failures: %v", err)
	}
	defer stream.Close()
	if err = stream.Write([]byte("retry")); err != nil {
		t.Fatalf("write retried stream: %v", err)
	}
	if got := readH2StreamText(t, stream); got != "ack:retry" {
		t.Fatalf("retried response = %q, want ack:retry", got)
	}
	if got := dialer.dials.Load(); got != 3 {
		t.Fatalf("TLS dial count = %d, want two retries then success", got)
	}
}

func newH2EchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 2 {
			http.Error(w, fmt.Sprintf("protocol=%s", request.Proto), http.StatusHTTPVersionNotSupported)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		buffer := make([]byte, 64)
		count, err := request.Body.Read(buffer)
		if err != nil || count == 0 {
			return
		}
		_, _ = w.Write([]byte("ack:" + string(buffer[:count])))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func readH2StreamText(t *testing.T, stream *H2Stream) string {
	t.Helper()
	result, err := readH2StreamTextResult(stream)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readH2StreamTextResult(stream *H2Stream) (string, error) {
	var result strings.Builder
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-stream.Data():
			if !ok {
				if err := stream.Err(); err != nil {
					return result.String(), fmt.Errorf("stream closed with error: %w", err)
				}
				return result.String(), nil
			}
			result.Write(chunk)
			if strings.HasPrefix(result.String(), "ack:") {
				return result.String(), nil
			}
		case <-timer.C:
			return result.String(), fmt.Errorf("timed out waiting for stream response; err=%v", stream.Err())
		}
	}
}
