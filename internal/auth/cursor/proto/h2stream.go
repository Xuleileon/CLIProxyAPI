package proto

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

var h2StreamSequence atomic.Uint64

var h2DialRetryDelays = [...]time.Duration{100 * time.Millisecond, 250 * time.Millisecond}

// H2StreamPool owns reusable HTTP/2 transports. Each transport multiplexes
// independent Connect RPCs over persistent TLS connections and delegates
// flow control, GOAWAY, RST_STREAM, and stream ID allocation to x/net/http2.
type H2StreamPool struct {
	mu         sync.Mutex
	transports map[string]*http2.Transport
	tlsConfig  *tls.Config
}

// NewH2StreamPool creates an empty reusable HTTP/2 transport pool.
func NewH2StreamPool() *H2StreamPool {
	return &H2StreamPool{transports: make(map[string]*http2.Transport)}
}

// H2Stream provides a full-duplex request and response body for one Connect
// RPC. Closing a stream resets only that HTTP/2 stream, not the shared TLS
// connection used by other requests.
type H2Stream struct {
	id  string
	ctx context.Context

	requestWriter *io.PipeWriter
	cancel        context.CancelFunc

	writeMu sync.Mutex
	errMu   sync.RWMutex
	err     error

	responseMu   sync.Mutex
	responseBody io.ReadCloser

	dataCh    chan []byte
	doneCh    chan struct{}
	closeOnce sync.Once
	frameNum  atomic.Int64
}

type h2RoundTripResult struct {
	response *http.Response
	err      error
}

// ID returns the unique identifier for this stream (for logging).
func (s *H2Stream) ID() string { return s.id }

// FrameNum returns the number of response-body chunks read from this stream.
func (s *H2Stream) FrameNum() int64 { return s.frameNum.Load() }

// DialH2Stream establishes a direct TLS+HTTP/2 stream without retaining a
// reusable pool. Long-lived callers should use H2StreamPool.Open instead.
func DialH2Stream(host string, headers map[string]string) (*H2Stream, error) {
	return DialH2StreamWithDialer(context.Background(), host, headers, nil)
}

// DialH2StreamWithDialer establishes a TLS+HTTP/2 stream through dialer.
// A nil dialer preserves direct-connect behavior.
func DialH2StreamWithDialer(ctx context.Context, host string, headers map[string]string, dialer proxy.Dialer) (*H2Stream, error) {
	pool := NewH2StreamPool()
	return pool.Open(ctx, host, host, headers, dialer)
}

// Open starts a full-duplex Connect RPC on a reusable HTTP/2 transport.
// poolKey must identify connection-affecting state such as endpoint, proxy,
// and credential isolation. Headers remain request-scoped.
func (p *H2StreamPool) Open(ctx context.Context, poolKey, host string, headers map[string]string, dialer proxy.Dialer) (*H2Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return nil, fmt.Errorf("h2: nil stream pool")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("h2: empty host")
	}
	path := headers[":path"]
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("h2: invalid path %q", path)
	}
	if poolKey == "" {
		poolKey = host
	}

	transport := p.transportFor(poolKey, dialer)
	requestReader, requestWriter := io.Pipe()
	streamCtx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	var readyState struct {
		sync.Mutex
		gotConn      bool
		wroteHeaders bool
		closed       bool
	}
	markReady := func(gotConn, wroteHeaders bool) {
		readyState.Lock()
		defer readyState.Unlock()
		readyState.gotConn = readyState.gotConn || gotConn
		readyState.wroteHeaders = readyState.wroteHeaders || wroteHeaders
		if readyState.gotConn && readyState.wroteHeaders && !readyState.closed {
			readyState.closed = true
			close(ready)
		}
	}
	var reused atomic.Bool

	requestURL := &url.URL{Scheme: "https", Host: host, Path: path}
	request := (&http.Request{
		Method:        http.MethodPost,
		URL:           requestURL,
		Header:        make(http.Header),
		Body:          requestReader,
		ContentLength: -1,
		Host:          host,
	}).WithContext(httptrace.WithClientTrace(streamCtx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused.Store(info.Reused)
			markReady(true, false)
		},
		WroteHeaders: func() {
			markReady(false, true)
		},
	}))
	for name, value := range headers {
		if strings.HasPrefix(name, ":") {
			continue
		}
		request.Header.Set(name, value)
	}

	streamID := h2StreamSequence.Add(1)
	stream := &H2Stream{
		id:            fmt.Sprintf("%d-%s", streamID, time.Now().Format("150405.000")),
		ctx:           streamCtx,
		requestWriter: requestWriter,
		cancel:        cancel,
		dataCh:        make(chan []byte, 256),
		doneCh:        make(chan struct{}),
	}
	resultCh := make(chan h2RoundTripResult, 1)
	go func() {
		response, err := transport.RoundTrip(request)
		resultCh <- h2RoundTripResult{response: response, err: err}
	}()
	go stream.readLoop(resultCh)

	select {
	case <-ready:
		log.Debugf("h2stream[%s]: request headers sent reused=%t", stream.id, reused.Load())
		return stream, nil
	case <-stream.Done():
		err := stream.Err()
		if err == nil {
			err = fmt.Errorf("h2: stream ended before request headers were sent")
		}
		stream.Close()
		return nil, err
	case <-ctx.Done():
		stream.Close()
		return nil, ctx.Err()
	}
}

// CloseIdleConnections closes pooled TLS connections that have no active
// streams. Active Connect RPCs are not interrupted.
func (p *H2StreamPool) CloseIdleConnections() {
	if p == nil {
		return
	}
	p.mu.Lock()
	transports := make([]*http2.Transport, 0, len(p.transports))
	for _, transport := range p.transports {
		transports = append(transports, transport)
	}
	p.mu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}

func (p *H2StreamPool) transportFor(poolKey string, dialer proxy.Dialer) *http2.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.transports == nil {
		p.transports = make(map[string]*http2.Transport)
	}
	if transport := p.transports[poolKey]; transport != nil {
		return transport
	}
	tlsConfig := p.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	transport := &http2.Transport{
		TLSClientConfig:    tlsConfig,
		DisableCompression: true,
		// Keep protocol liveness under the Connect heartbeat. No network
		// timeout is applied after a Cursor Run has been established.
		IdleConnTimeout:  0,
		ReadIdleTimeout:  0,
		WriteByteTimeout: 0,
		DialTLSContext: func(ctx context.Context, network, address string, config *tls.Config) (net.Conn, error) {
			return dialCursorH2TLS(ctx, network, address, config, dialer)
		},
		CountError: func(errorType string) {
			log.Debugf("h2: transport error type=%s", errorType)
		},
	}
	p.transports[poolKey] = transport
	return transport
}

func dialCursorH2TLS(ctx context.Context, network, address string, config *tls.Config, dialer proxy.Dialer) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt <= len(h2DialRetryDelays); attempt++ {
		conn, err := dialCursorH2TLSOnce(ctx, network, address, config, dialer)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if attempt == len(h2DialRetryDelays) || !isRetryableH2DialError(err) {
			return nil, err
		}

		delay := h2DialRetryDelays[attempt]
		log.Debugf("h2: connection attempt %d failed, retrying in %s: %v", attempt+1, delay, err)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func dialCursorH2TLSOnce(ctx context.Context, network, address string, config *tls.Config, dialer proxy.Dialer) (net.Conn, error) {
	var (
		rawConn net.Conn
		err     error
	)
	if dialer == nil {
		rawConn, err = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	} else if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		rawConn, err = contextDialer.DialContext(ctx, network, address)
	} else {
		rawConn, err = dialer.Dial(network, address)
	}
	if err != nil {
		return nil, fmt.Errorf("h2: TCP dial failed: %w", err)
	}

	tlsConfig := &tls.Config{}
	if config != nil {
		tlsConfig = config.Clone()
	}
	if tlsConfig.ServerName == "" {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			host = address
		}
		tlsConfig.ServerName = strings.Trim(host, "[]")
	}
	tlsConfig.NextProtos = []string{"h2"}
	tlsConn := tls.Client(rawConn, tlsConfig)
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("h2: TLS dial failed: %w", err)
	}
	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("h2: server did not negotiate h2")
	}
	return tlsConn, nil
}

func isRetryableH2DialError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET)
}

// Write writes request-body bytes to this Connect RPC. Concurrent heartbeat
// and tool-result writes are serialized per stream.
func (s *H2Stream) Write(data []byte) error {
	if s == nil || s.requestWriter == nil {
		return fmt.Errorf("h2: stream is not writable")
	}
	select {
	case <-s.doneCh:
		if err := s.Err(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	default:
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.requestWriter.Write(data); err != nil {
		if streamErr := s.Err(); streamErr != nil {
			return streamErr
		}
		return fmt.Errorf("h2: request body write failed: %w", err)
	}
	return nil
}

// Data returns the channel of received response-body chunks.
func (s *H2Stream) Data() <-chan []byte { return s.dataCh }

// Done returns a channel closed when the stream ends.
func (s *H2Stream) Done() <-chan struct{} { return s.doneCh }

// Err returns the error that caused the stream to close, or nil after EOF.
func (s *H2Stream) Err() error {
	if s == nil {
		return nil
	}
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}

// Close cancels only this HTTP/2 stream. The transport keeps healthy shared
// TLS connections available for other and future Cursor Runs.
func (s *H2Stream) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.requestWriter != nil {
			_ = s.requestWriter.Close()
		}
		s.responseMu.Lock()
		responseBody := s.responseBody
		s.responseMu.Unlock()
		if responseBody != nil {
			_ = responseBody.Close()
		}
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *H2Stream) readLoop(resultCh <-chan h2RoundTripResult) {
	defer close(s.doneCh)
	defer close(s.dataCh)

	result := <-resultCh
	if result.err != nil {
		s.setErr(result.err)
		log.Debugf("h2stream[%s]: round trip error: %v", s.id, result.err)
		return
	}
	if result.response == nil {
		err := fmt.Errorf("h2: empty response")
		s.setErr(err)
		return
	}
	s.responseMu.Lock()
	s.responseBody = result.response.Body
	s.responseMu.Unlock()
	defer result.response.Body.Close()

	if result.response.StatusCode < http.StatusOK || result.response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(result.response.Body, 4096))
		err := fmt.Errorf("h2: upstream returned %s: %s", result.response.Status, strings.TrimSpace(string(body)))
		s.setErr(err)
		return
	}

	buffer := make([]byte, 32*1024)
	for {
		count, err := result.response.Body.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			s.frameNum.Add(1)
			select {
			case s.dataCh <- chunk:
			case <-s.doneContext():
				return
			}
		}
		if err != nil {
			if err != io.EOF && !isClosedStreamError(err) {
				s.setErr(err)
				log.Debugf("h2stream[%s]: response read error: %v", s.id, err)
			}
			return
		}
	}
}

func (s *H2Stream) setErr(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.errMu.Unlock()
}

func (s *H2Stream) doneContext() <-chan struct{} {
	if s.ctx == nil {
		return nil
	}
	return s.ctx.Done()
}

func isClosedStreamError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "request canceled") ||
		strings.Contains(message, "context canceled") ||
		strings.Contains(message, "closed response body") ||
		strings.Contains(message, "response body closed")
}
