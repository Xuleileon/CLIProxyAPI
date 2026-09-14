package helps

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestOpenCodeResponsesReusesCompletedConnection(t *testing.T) {
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
	if len(peers) != 2 || peers[0] != peers[1] {
		t.Fatalf("completed connection was not reused: %v", peers)
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

func TestOpenCodeResponsesIsolatesActiveAndCanceledStreams(t *testing.T) {
	peers := make(chan string, 3)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers <- r.RemoteAddr
		if r.URL.Query().Get("blocked") == "1" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "completed")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/responses?blocked=1", nil)
	blocked, err := DoOpenCodeGoRequest(ctx, client, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocked.Body.Close() }()
	firstPeer := <-peers
	var completedPeer string
	for i := range 2 {
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/responses", nil)
		resp, errRequest := DoOpenCodeGoRequest(ctx, client, req)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		_, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if errRead != nil {
			t.Fatal(errRead)
		}
		peer := <-peers
		if peer == firstPeer {
			t.Fatal("active or canceled connection reused")
		}
		if i == 0 {
			completedPeer = peer
			if errClose := blocked.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
		} else if peer != completedPeer {
			t.Fatal("healthy connection discarded")
		}
	}
}

func TestOpenCodeResponsesDiscardsTruncatedBody(t *testing.T) {
	peers := make(chan string, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers <- r.RemoteAddr
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	for range 2 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/responses", nil)
		resp, err := DoOpenCodeGoRequest(req.Context(), client, req)
		if err != nil {
			t.Fatal(err)
		}
		_, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead == nil {
			t.Fatal("expected truncated response")
		}
	}
	if <-peers == <-peers {
		t.Fatal("failed connection reused")
	}
}

func TestOpenCodePoolEvictionDiscardsReturnedLease(t *testing.T) {
	p := &openCodeGoPool{}
	transport := p.acquire(http.DefaultTransport.(*http.Transport))
	p.close()
	p.release(transport, true)
	if len(p.idle) != 0 {
		t.Fatal("evicted pool retained a late lease")
	}
}

func TestOpenCodeResponsesReusesAfterTerminalClose(t *testing.T) {
	peers := make(chan string, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peers <- r.RemoteAddr
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	for range 2 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/responses", nil)
		resp, err := DoOpenCodeGoRequest(req.Context(), client, req)
		if err != nil {
			t.Fatal(err)
		}
		if _, errRead := bufio.NewReader(resp.Body).ReadString('\n'); errRead != nil {
			t.Fatal(errRead)
		}
		MarkOpenCodeGoResponseComplete(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
	}
	if <-peers != <-peers {
		t.Fatal("completed SSE stream connection not reused")
	}
}
