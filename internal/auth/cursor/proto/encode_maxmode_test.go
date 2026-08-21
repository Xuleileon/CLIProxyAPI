package proto

import (
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeRunRequestSetsMaxModeFalse(t *testing.T) {
	raw := EncodeRunRequest(&RunRequestParams{
		ModelId:        "composer-2.5",
		MaxMode:        false,
		UserText:       "hi",
		MessageId:      "msg-1",
		ConversationId: "conv-1",
		SystemPrompt:   "sys",
		BlobStore:      map[string][]byte{},
	})
	if len(raw) == 0 {
		t.Fatal("empty encode output")
	}

	// AgentClientMessage.field 1 = run_request
	runReq := mustFindBytesField(t, raw, ACM_RunRequest)
	md := mustFindBytesField(t, runReq, ARR_ModelDetails)
	rm := mustFindBytesField(t, runReq, ARR_RequestedModel)

	// ModelDetails.max_mode is proto3-optional: false must still be present on the wire
	// so Cursor does not treat an unset field as Max Mode for CLI clients.
	if got, ok := findBoolField(md, MD_MaxMode); !ok {
		t.Fatal("ModelDetails.max_mode missing; Cursor may default CLI to Max Mode")
	} else if got {
		t.Fatal("ModelDetails.max_mode = true, want false")
	}

	// RequestedModel.max_mode is a plain proto3 bool: false is omitted on the wire.
	if got, ok := findBoolField(rm, RM_MaxMode); ok && got {
		t.Fatal("RequestedModel.max_mode = true, want false/absent")
	}

	if id, ok := findStringField(md, MD_ModelId); !ok || id != "composer-2.5" {
		t.Fatalf("ModelDetails.model_id = %q ok=%v, want composer-2.5", id, ok)
	}
	if id, ok := findStringField(rm, RM_ModelId); !ok || id != "composer-2.5" {
		t.Fatalf("RequestedModel.model_id = %q ok=%v, want composer-2.5", id, ok)
	}
}

func TestEncodeRunRequestSetsMaxModeTrue(t *testing.T) {
	raw := EncodeRunRequest(&RunRequestParams{
		ModelId:   "composer-2.5",
		MaxMode:   true,
		UserText:  "hi",
		MessageId: "msg-1",
		BlobStore: map[string][]byte{},
	})
	runReq := mustFindBytesField(t, raw, ACM_RunRequest)
	md := mustFindBytesField(t, runReq, ARR_ModelDetails)
	if got, ok := findBoolField(md, MD_MaxMode); !ok || !got {
		t.Fatalf("ModelDetails.max_mode = %v ok=%v, want true", got, ok)
	}
}

func TestSetStrRepairsInvalidUTF8(t *testing.T) {
	t.Parallel()

	msg := newMsg("UserMessage")
	setStr(msg, "text", "before\xffafter")
	got := msg.Get(field(msg, "text")).String()
	if !utf8.ValidString(got) {
		t.Fatal("protobuf string contains invalid UTF-8")
	}
	if got != "before\uFFFDafter" {
		t.Fatalf("protobuf string = %q, want %q", got, "before\uFFFDafter")
	}
	if raw := marshal(msg); len(raw) == 0 {
		t.Fatal("empty protobuf output")
	}
}

func mustFindBytesField(t *testing.T, b []byte, num protowire.Number) []byte {
	t.Helper()
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			t.Fatalf("bad tag in protobuf blob")
		}
		b = b[nTag:]
		if typ != protowire.BytesType {
			nSkip := protowire.ConsumeFieldValue(n, typ, b)
			if nSkip < 0 {
				t.Fatalf("bad field value")
			}
			b = b[nSkip:]
			continue
		}
		val, nVal := protowire.ConsumeBytes(b)
		if nVal < 0 {
			t.Fatalf("bad bytes field")
		}
		b = b[nVal:]
		if n == num {
			return append([]byte(nil), val...)
		}
	}
	t.Fatalf("field %d not found", num)
	return nil
}

func findBoolField(b []byte, num protowire.Number) (bool, bool) {
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			return false, false
		}
		b = b[nTag:]
		if typ == protowire.VarintType {
			v, nVal := protowire.ConsumeVarint(b)
			if nVal < 0 {
				return false, false
			}
			b = b[nVal:]
			if n == num {
				return v != 0, true
			}
			continue
		}
		nSkip := protowire.ConsumeFieldValue(n, typ, b)
		if nSkip < 0 {
			return false, false
		}
		b = b[nSkip:]
	}
	return false, false
}

func findStringField(b []byte, num protowire.Number) (string, bool) {
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			return "", false
		}
		b = b[nTag:]
		if typ == protowire.BytesType {
			val, nVal := protowire.ConsumeBytes(b)
			if nVal < 0 {
				return "", false
			}
			b = b[nVal:]
			if n == num {
				return string(val), true
			}
			continue
		}
		nSkip := protowire.ConsumeFieldValue(n, typ, b)
		if nSkip < 0 {
			return "", false
		}
		b = b[nSkip:]
	}
	return "", false
}
