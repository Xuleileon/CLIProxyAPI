package proto

import (
	"context"
	"errors"
	"net"
	"testing"
)

type recordingContextDialer struct {
	network string
	address string
	usedCtx bool
	err     error
}

func (d *recordingContextDialer) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("unexpected Dial call")
}

func (d *recordingContextDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.network = network
	d.address = address
	d.usedCtx = true
	return nil, d.err
}

func TestDialH2StreamWithDialerUsesProvidedContextDialer(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("proxy dial stopped")
	dialer := &recordingContextDialer{err: sentinel}
	_, err := DialH2StreamWithDialer(context.Background(), "api2.cursor.sh", nil, dialer)
	if !errors.Is(err, sentinel) {
		t.Fatalf("DialH2StreamWithDialer error = %v, want wrapped sentinel", err)
	}
	if !dialer.usedCtx {
		t.Fatal("provided context dialer was not used")
	}
	if dialer.network != "tcp" || dialer.address != "api2.cursor.sh:443" {
		t.Fatalf("dial target = %s %s, want tcp api2.cursor.sh:443", dialer.network, dialer.address)
	}
}
