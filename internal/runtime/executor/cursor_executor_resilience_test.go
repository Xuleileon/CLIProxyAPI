package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCursorAdmissionLimits(t *testing.T) {
	t.Parallel()

	active, queued := cursorAdmissionLimits(nil)
	if active != cursorDefaultActiveRuns || queued != cursorDefaultQueuedRuns {
		t.Fatalf("default limits = %d/%d, want %d/%d", active, queued, cursorDefaultActiveRuns, cursorDefaultQueuedRuns)
	}

	active, queued = cursorAdmissionLimits(&config.Config{Cursor: config.CursorConfig{
		MaxConcurrentRuns: 7,
		MaxQueuedRuns:     19,
	}})
	if active != 7 || queued != 19 {
		t.Fatalf("configured limits = %d/%d, want 7/19", active, queued)
	}
}

func TestClassifyCursorStreamFailure(t *testing.T) {
	t.Parallel()

	upstreamErr := &cursorproto.ConnectError{Code: "unavailable", Message: "server is shutting down"}
	for _, test := range []struct {
		name       string
		sessionErr error
		dataSent   bool
		wantKind   cursorStreamFailureKind
		wantStatus int
	}{
		{name: "before data retries", wantKind: cursorStreamFailureRetry, wantStatus: http.StatusServiceUnavailable},
		{name: "after data is terminal", dataSent: true, wantKind: cursorStreamFailureTerminal, wantStatus: http.StatusServiceUnavailable},
		{name: "local cancellation never retries", sessionErr: context.Canceled, dataSent: true, wantKind: cursorStreamFailureCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, err := classifyCursorStreamFailure(upstreamErr, test.sessionErr, test.dataSent)
			if kind != test.wantKind {
				t.Fatalf("kind = %d, want %d", kind, test.wantKind)
			}
			if test.sessionErr != nil {
				if !errors.Is(err, test.sessionErr) {
					t.Fatalf("error = %v, want %v", err, test.sessionErr)
				}
				return
			}
			statusErr, ok := err.(cliproxyexecutor.StatusError)
			if !ok || statusErr.StatusCode() != test.wantStatus {
				t.Fatalf("status error = %#v, want status %d", err, test.wantStatus)
			}
		})
	}
}

func TestCursorStreamClosedWithoutTerminalFrameIsUnexpectedEOF(t *testing.T) {
	t.Parallel()

	stream := &fakeCursorToolResultStream{}
	if err := cursorStreamClosedError(stream); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("closed stream error = %v, want unexpected EOF", err)
	}
}

func TestClassifyCursorTransportErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "graceful shutdown", err: errors.New("http2: Transport received Server's graceful shutdown GOAWAY"), wantStatus: http.StatusServiceUnavailable},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, wantStatus: http.StatusBadGateway},
		{name: "peer h2 internal error", err: errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer"), wantStatus: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			classified := classifyCursorError(test.err)
			statusErr, ok := classified.(cliproxyexecutor.StatusError)
			if !ok || statusErr.StatusCode() != test.wantStatus {
				t.Fatalf("classified error = %#v, want status %d", classified, test.wantStatus)
			}
		})
	}
}
