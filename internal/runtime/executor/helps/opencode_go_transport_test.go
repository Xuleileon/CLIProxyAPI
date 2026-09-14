package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"golang.org/x/net/http2"
)

func TestOpenCodeResponsesDoesNotReuseSharedConnection(t *testing.T) {
	var mu sync.Mutex
	var peers []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers = append(peers, r.RemoteAddr)
		mu.Unlock()
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	for range 2 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/responses", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := DoOpenCodeGoRequest(req.Context(), client, req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.ProtoMajor != 2 {
			t.Fatalf("expected HTTP/2, got %s", resp.Proto)
		}
		_, err = io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(peers) != 2 || peers[0] == peers[1] {
		t.Fatalf("requests reused connection: %v", peers)
	}
	if client.Transport.(*http.Transport).DisableKeepAlives {
		t.Fatal("shared transport mutated")
	}
}

func TestOpenCodeHTTP2ResetIsNotReplayed(t *testing.T) {
	err := openCodeGoTracedConnectionError(http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}, false)
	typed, ok := err.(openCodeGoConnectionError)
	if !ok || !typed.IsRequestScoped() || typed.RetryAfter() != nil {
		t.Fatalf("post-connect error must not replay: %v", err)
	}
}
