package helps

import (
	"bytes"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenCodeGoCompactionPreservesConversation(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","content":[{"type":"compaction","content":null}]},{"role":"assistant","content":[{"type":"compaction","content":"First summary","signature":"opaque"},{"type":"text","text":"then continue"},{"type":"compaction","content":"Second summary"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}]}`)
	before := bytes.Clone(raw)
	req, opts := NormalizeOpenCodeGoRequest(cliproxyexecutor.Request{Payload: raw}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: raw})
	if !bytes.Equal(raw, before) || !bytes.Equal(req.Payload, opts.OriginalRequest) {
		t.Fatal("request was mutated in place or original request was not normalized")
	}
	for path, want := range map[string]string{"messages.#": "2", "messages.0.content.0.text": "First summary", "messages.0.content.1.text": "then continue", "messages.0.content.2.text": "Second summary", "messages.1.content.0.tool_use_id": "call_1"} {
		if got := gjson.GetBytes(req.Payload, path).String(); got != want {
			t.Errorf("%s=%q, want %q", path, got, want)
		}
	}
	if gjson.GetBytes(req.Payload, "messages.0.content.0.signature").Exists() {
		t.Fatal("Anthropic compaction signature leaked to text")
	}
}

func TestOpenCodeGoCompactionLeavesNativeAndPlainHistoryUnchanged(t *testing.T) {
	for _, tc := range []struct {
		source sdktranslator.Format
		raw    string
	}{
		{sdktranslator.FormatOpenAIResponse, `{"input":[{"type":"compaction","encrypted_content":"opaque"}],"context_management":[{"type":"compaction"}]}`},
		{sdktranslator.FormatClaude, `{"messages":[{"role":"user","content":"Earlier conversation summary: MAPLE_72"}]}`},
		{sdktranslator.FormatClaude, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","input":{"type":"compaction","content":"business data"}}]}]}`},
	} {
		req, _ := NormalizeOpenCodeGoRequest(cliproxyexecutor.Request{Payload: []byte(tc.raw)}, cliproxyexecutor.Options{SourceFormat: tc.source})
		if string(req.Payload) != tc.raw {
			t.Fatalf("unrelated payload changed: %s", req.Payload)
		}
	}
}
