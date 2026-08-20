package executor

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type cursorProxyObservation struct {
	method string
	host   string
	err    error
}

func startRejectingCursorProxy(t *testing.T) (string, <-chan cursorProxyObservation) {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })

	observed := make(chan cursorProxyObservation, 1)
	go func() {
		connection, errAccept := listener.Accept()
		if errAccept != nil {
			observed <- cursorProxyObservation{err: errAccept}
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		request, errRead := http.ReadRequest(bufio.NewReader(connection))
		if errRead != nil {
			observed <- cursorProxyObservation{err: errRead}
			return
		}
		observed <- cursorProxyObservation{method: request.Method, host: request.Host}
		_, _ = fmt.Fprint(connection, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
	}()

	return "http://" + listener.Addr().String(), observed
}

func assertCursorProxyObserved(t *testing.T, observed <-chan cursorProxyObservation) {
	t.Helper()
	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatalf("proxy observation failed: %v", got.err)
		}
		if got.method != http.MethodConnect || got.host != "api2.cursor.sh:443" {
			t.Fatalf("proxy request = %s %s, want CONNECT api2.cursor.sh:443", got.method, got.host)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not receive a Cursor CONNECT request")
	}
}

func TestCursorProxyURLPrefersAuthOverride(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.ProxyURL = "http://global.example.com:8080"
	auth := &cliproxyauth.Auth{ProxyURL: "socks5://account.example.com:1080"}
	if got := cursorProxyURL(cfg, auth); got != auth.ProxyURL {
		t.Fatalf("cursorProxyURL = %q, want auth override %q", got, auth.ProxyURL)
	}

	auth.ProxyURL = ""
	if got := cursorProxyURL(cfg, auth); got != cfg.ProxyURL {
		t.Fatalf("cursorProxyURL = %q, want global proxy %q", got, cfg.ProxyURL)
	}

	auth.ProxyURL = "direct"
	if got := cursorProxyURL(cfg, auth); got != "direct" {
		t.Fatalf("cursorProxyURL = %q, want direct override", got)
	}
}

func TestOpenCursorH2StreamUsesAuthHTTPProxy(t *testing.T) {
	proxyURL, observed := startRejectingCursorProxy(t)
	cfg := &config.Config{}
	cfg.ProxyURL = "invalid-global-proxy"
	auth := &cliproxyauth.Auth{ProxyURL: proxyURL}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := openCursorH2Stream(ctx, cfg, auth, "access-token")
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("openCursorH2Stream error = %v, want proxy 502", err)
	}
	assertCursorProxyObserved(t, observed)
}

func TestFetchCursorModelsUsesConfiguredHTTPProxy(t *testing.T) {
	withIsolatedCursorModelsCache(t)
	proxyURL, observed := startRejectingCursorProxy(t)
	auth := &cliproxyauth.Auth{
		ID:       "cursor-proxy-models",
		ProxyURL: proxyURL,
		Metadata: map[string]any{"access_token": "access-token"},
	}

	got := FetchCursorModels(context.Background(), auth, nil)
	if len(got) == 0 {
		t.Fatal("FetchCursorModels returned no fallback models")
	}
	assertCursorProxyObserved(t, observed)
}

func TestCursorRefreshUsesAuthHTTPProxy(t *testing.T) {
	proxyURL, observed := startRejectingCursorProxy(t)
	cfg := &config.Config{}
	cfg.ProxyURL = "invalid-global-proxy"
	auth := &cliproxyauth.Auth{
		ID:       "cursor-proxy-refresh",
		ProxyURL: proxyURL,
		Metadata: map[string]any{"refresh_token": "refresh-token"},
	}

	executor := NewCursorExecutor(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := executor.Refresh(ctx, auth)
	if err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("CursorExecutor.Refresh error = %v, want proxy Bad Gateway", err)
	}
	assertCursorProxyObserved(t, observed)
}
