package cursor

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPClientProxyModes(t *testing.T) {
	t.Parallel()

	proxied, err := NewHTTPClient("http://proxy.example.com:8080", 3*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPClient(proxy) returned error: %v", err)
	}
	transport, ok := proxied.Transport.(*http.Transport)
	if !ok || transport == nil || transport.Proxy == nil {
		t.Fatal("proxied client is missing an HTTP proxy transport")
	}
	req, errRequest := http.NewRequest(http.MethodGet, "https://api2.cursor.sh", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://proxy.example.com:8080", proxyURL)
	}
	if proxied.Timeout != 3*time.Second {
		t.Fatalf("timeout = %s, want 3s", proxied.Timeout)
	}

	direct, err := NewHTTPClient("direct", 0)
	if err != nil {
		t.Fatalf("NewHTTPClient(direct) returned error: %v", err)
	}
	directTransport, ok := direct.Transport.(*http.Transport)
	if !ok || directTransport == nil {
		t.Fatal("direct client is missing an explicit transport")
	}
	if directTransport.Proxy != nil {
		t.Fatal("direct client unexpectedly uses a proxy")
	}

	inherited, err := NewHTTPClient("", 0)
	if err != nil {
		t.Fatalf("NewHTTPClient(inherit) returned error: %v", err)
	}
	if inherited.Transport != nil {
		t.Fatalf("inherited client transport = %T, want nil", inherited.Transport)
	}

	if _, err = NewHTTPClient("localhost:7897", 0); err == nil {
		t.Fatal("NewHTTPClient accepted a proxy URL without a scheme")
	}
}
