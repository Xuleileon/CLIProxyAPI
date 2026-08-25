package proto

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeRunRequestCarriesSelectedImages(t *testing.T) {
	t.Parallel()
	images := []ImageData{
		{MimeType: "image/png", Data: []byte{1, 2, 3}},
		{MimeType: "image/jpeg", Data: []byte{4, 5, 6}},
	}
	checkpoint := protowire.AppendTag(nil, CSS_Mode, protowire.VarintType)
	checkpoint = protowire.AppendVarint(checkpoint, uint64(AgentModeAgent))
	for _, test := range []struct {
		name       string
		checkpoint []byte
	}{
		{name: "cold"},
		{name: "checkpoint", checkpoint: checkpoint},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := EncodeRunRequest(&RunRequestParams{
				ModelId:       "composer-2.5-fast",
				UserText:      "inspect",
				MessageId:     "message",
				Images:        images,
				RawCheckpoint: test.checkpoint,
				BlobStore:     map[string][]byte{},
			})
			runRequest := mustFindBytesField(t, raw, ACM_RunRequest)
			action := mustFindBytesField(t, runRequest, ARR_Action)
			userAction := mustFindBytesField(t, action, CA_UserMessageAction)
			userMessage := mustFindBytesField(t, userAction, UMA_UserMessage)
			selectedContext := mustFindBytesField(t, userMessage, UM_SelectedContext)
			selectedImages := findAllBytesFields(t, selectedContext, SC_SelectedImages)
			if len(selectedImages) != len(images) {
				t.Fatalf("selected images = %d, want %d", len(selectedImages), len(images))
			}
			for index, selectedImage := range selectedImages {
				if mime, ok := findStringField(selectedImage, SI_MimeType); !ok || mime != images[index].MimeType {
					t.Fatalf("image %d mime = %q, found=%t", index, mime, ok)
				}
				if data := mustFindBytesField(t, selectedImage, SI_Data); !bytes.Equal(data, images[index].Data) {
					t.Fatalf("image %d data = %v, want %v", index, data, images[index].Data)
				}
			}
		})
	}
}

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

func TestEncodeRunRequestSetsMode(t *testing.T) {
	t.Parallel()

	raw := EncodeRunRequest(&RunRequestParams{
		ModelId:   "composer-2.5",
		UserText:  "hi",
		MessageId: "msg-1",
		AgentMode: AgentModeAsk,
		BlobStore: map[string][]byte{},
	})
	runReq := mustFindBytesField(t, raw, ACM_RunRequest)
	state := mustFindBytesField(t, runReq, ARR_ConversationState)
	if got, ok := findVarintField(state, CSS_Mode); !ok || got != uint64(AgentModeAsk) {
		t.Fatalf("ConversationStateStructure.mode = %d ok=%v, want %d", got, ok, AgentModeAsk)
	}
}

func TestEncodeRunRequestOverridesCheckpointMode(t *testing.T) {
	t.Parallel()

	checkpoint := protowire.AppendTag(nil, CSS_Mode, protowire.VarintType)
	checkpoint = protowire.AppendVarint(checkpoint, uint64(AgentModeAgent))
	raw := EncodeRunRequest(&RunRequestParams{
		ModelId:       "composer-2.5",
		UserText:      "hi",
		MessageId:     "msg-1",
		AgentMode:     AgentModeAsk,
		RawCheckpoint: checkpoint,
	})
	runReq := mustFindBytesField(t, raw, ACM_RunRequest)
	state := mustFindBytesField(t, runReq, ARR_ConversationState)
	if got, ok := findLastVarintField(state, CSS_Mode); !ok || got != uint64(AgentModeAsk) {
		t.Fatalf("last ConversationStateStructure.mode = %d ok=%v, want %d", got, ok, AgentModeAsk)
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

func findAllBytesFields(t *testing.T, b []byte, num protowire.Number) [][]byte {
	t.Helper()
	var values [][]byte
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			t.Fatal("bad tag in protobuf blob")
		}
		b = b[nTag:]
		if typ == protowire.BytesType {
			value, nValue := protowire.ConsumeBytes(b)
			if nValue < 0 {
				t.Fatal("bad bytes field")
			}
			b = b[nValue:]
			if n == num {
				values = append(values, append([]byte(nil), value...))
			}
			continue
		}
		nSkip := protowire.ConsumeFieldValue(n, typ, b)
		if nSkip < 0 {
			t.Fatal("bad field value")
		}
		b = b[nSkip:]
	}
	return values
}

func findBoolField(b []byte, num protowire.Number) (bool, bool) {
	v, ok := findVarintField(b, num)
	return v != 0, ok
}

func findVarintField(b []byte, num protowire.Number) (uint64, bool) {
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			return 0, false
		}
		b = b[nTag:]
		if typ == protowire.VarintType {
			v, nVal := protowire.ConsumeVarint(b)
			if nVal < 0 {
				return 0, false
			}
			b = b[nVal:]
			if n == num {
				return v, true
			}
			continue
		}
		nSkip := protowire.ConsumeFieldValue(n, typ, b)
		if nSkip < 0 {
			return 0, false
		}
		b = b[nSkip:]
	}
	return 0, false
}

func findLastVarintField(b []byte, num protowire.Number) (uint64, bool) {
	var found uint64
	var ok bool
	for len(b) > 0 {
		n, typ, nTag := protowire.ConsumeTag(b)
		if nTag < 0 {
			return 0, false
		}
		b = b[nTag:]
		if typ == protowire.VarintType {
			v, nVal := protowire.ConsumeVarint(b)
			if nVal < 0 {
				return 0, false
			}
			b = b[nVal:]
			if n == num {
				found, ok = v, true
			}
			continue
		}
		nSkip := protowire.ConsumeFieldValue(n, typ, b)
		if nSkip < 0 {
			return 0, false
		}
		b = b[nSkip:]
	}
	return found, ok
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
