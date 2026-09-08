package executor

import (
	"context"
	"errors"
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func TestCursorKVReadReplyFailureTerminatesRun(t *testing.T) {
	writeErr := errors.New("fixture write failure")
	kv := appendCursorTestVarint(nil, cursorproto.KSM_Id, 41)
	kv = appendCursorTestBytes(kv, cursorproto.KSM_GetBlobArgs, appendCursorTestBytes(nil, cursorproto.GBA_BlobId, []byte("blob")))
	payload := appendCursorTestBytes(nil, cursorproto.ASM_KvServerMessage, kv)
	ended := appendCursorTestBytes(nil, cursorproto.ASM_InteractionUpdate, appendCursorTestBytes(nil, 14, nil))
	stream := &fakeCursorToolResultStream{dataCh: make(chan []byte, 1), doneCh: make(chan struct{}), writeErr: writeErr}
	frames := cursorproto.FrameConnectMessage(payload, 0)
	frames = append(frames, cursorproto.FrameConnectMessage(ended, 0)...)
	stream.dataCh <- frames
	err := processH2SessionFrames(context.Background(), stream, map[string][]byte{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, writeErr) {
		t.Fatalf("Run error = %v, want original reply failure", err)
	}
}
