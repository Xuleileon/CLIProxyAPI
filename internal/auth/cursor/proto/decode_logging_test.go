package proto

import (
	"bytes"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestDecodeInteractionUpdateDebugLogIsBounded(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.GetLevel()
	defer func() {
		logger.SetOutput(previousOutput)
		logger.SetLevel(previousLevel)
	}()

	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetLevel(log.DebugLevel)

	payload := bytes.Repeat([]byte{0xff}, 64*1024)
	decodeInteractionUpdate(payload, &DecodedServerMessage{})

	if got := output.Len(); got > 1024 {
		t.Fatalf("debug output length = %d, want at most 1024 bytes", got)
	}
	if strings.Contains(output.String(), strings.Repeat("ff", 64)) {
		t.Fatalf("debug output contains an unbounded payload prefix: %q", output.String())
	}
	if !strings.Contains(output.String(), "truncated=true") {
		t.Fatalf("debug output does not disclose truncation: %q", output.String())
	}
}
