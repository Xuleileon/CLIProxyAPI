package helps

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
)

// Each transport is leased to one request until its body is closed. Unlike a
// shared HTTP/2 pool, a stalled stream cannot capture other active requests.
var openCodeGoPools = cache.NewBoundedLRU[*http.Transport, *openCodeGoPool](32,
	func(_ *http.Transport, pool *openCodeGoPool) { pool.close() })

type openCodeGoPool struct {
	mu     sync.Mutex
	idle   []*http.Transport
	closed bool
}

func (p *openCodeGoPool) acquire(base *http.Transport) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.idle); n > 0 {
		transport := p.idle[n-1]
		p.idle = p.idle[:n-1]
		return transport
	}
	return base.Clone()
}

func (p *openCodeGoPool) release(transport *http.Transport, reusable bool) {
	p.mu.Lock()
	if reusable && !p.closed && len(p.idle) < 4 {
		p.idle = append(p.idle, transport)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	transport.CloseIdleConnections()
}

func (p *openCodeGoPool) close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, transport := range idle {
		transport.CloseIdleConnections()
	}
}

type openCodeGoLeasedBody struct {
	io.ReadCloser
	pool      *openCodeGoPool
	transport *http.Transport
	complete  atomic.Bool
	failed    atomic.Bool
	once      sync.Once
	closeErr  error
}

// MarkOpenCodeGoResponseComplete permits reuse after a parsed terminal SSE
// event. HTTP/2 can close that stream without waiting for response-body EOF.
func MarkOpenCodeGoResponseComplete(body io.ReadCloser) {
	if leased, ok := body.(*openCodeGoLeasedBody); ok {
		leased.complete.Store(true)
	}
}

func (b *openCodeGoLeasedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if err == io.EOF {
		b.complete.Store(true)
	} else if err != nil {
		b.failed.Store(true)
	}
	return n, err
}

func (b *openCodeGoLeasedBody) Close() error {
	b.once.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.pool.release(b.transport, b.complete.Load() && !b.failed.Load() && b.closeErr == nil)
	})
	return b.closeErr
}
