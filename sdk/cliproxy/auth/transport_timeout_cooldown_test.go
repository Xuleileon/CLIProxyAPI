package auth

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

type diagnosticGatewayTransportError struct{ error }

func (e diagnosticGatewayTransportError) Unwrap() error               { return e.error }
func (e diagnosticGatewayTransportError) StatusCode() int             { return 502 }
func (e diagnosticGatewayTransportError) IsConnectionLifecycle() bool { return true }
func (e diagnosticGatewayTransportError) RetryAfter() *time.Duration {
	d := 250 * time.Millisecond
	return &d
}

func TestTLSHandshakeFailureDoesNotCooldown(t *testing.T) {
	for _, traced := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "opencode"}[traced], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			defer close(done)
			go func() {
				conn, e := listener.Accept()
				if e == nil {
					defer conn.Close()
					<-done
				}
			}()
			transport := &http.Transport{TLSHandshakeTimeout: 50 * time.Millisecond}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			req, _ := http.NewRequest(http.MethodPost, "https://"+listener.Addr().String()+"/responses", nil)
			if traced {
				_, err = client.Do(req)
				if err != nil {
					err = diagnosticGatewayTransportError{err}
				}
			} else {
				_, err = client.Do(req)
			}
			if err == nil {
				t.Fatal("expected TLS handshake failure")
			}
			result := resultErrorFromError(err)
			if !shouldSkipCredentialCooldown(result) {
				t.Fatalf("transport failure classified as credential failure: %+v", result)
			}
			if traced {
				if result.HTTPStatus != 502 {
					t.Fatalf("status=%d", result.HTTPStatus)
				}
				retry, ok := err.(interface{ RetryAfter() *time.Duration })
				if !ok || retry.RetryAfter() == nil {
					t.Fatal("acquisition retry missing")
				}
			}
			m := NewManager(nil, nil, nil)
			a := &Auth{ID: "local-timeout", Provider: "opencode-go"}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: "test", Error: result})
			assertNoCooldown(t, m, a.ID, "test")
		})
	}
}

func TestExplicitUpstreamStatusesStillCooldown(t *testing.T) {
	for _, status := range []int{401, 403, 429, 502} {
		if shouldSkipCredentialCooldown(resultErrorFromError(&Error{HTTPStatus: status, Message: "net/http: TLS handshake timeout"})) {
			t.Fatalf("upstream %d bypassed cooldown", status)
		}
	}
}
