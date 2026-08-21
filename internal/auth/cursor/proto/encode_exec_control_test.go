package proto

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeExecStreamClose(t *testing.T) {
	raw := EncodeExecStreamClose(42)
	control := mustFindBytesField(t, raw, ACM_ExecClientControlMsg)
	streamClose := mustFindBytesField(t, control, 1)

	field, wireType, consumed := protowire.ConsumeTag(streamClose)
	if consumed < 0 {
		t.Fatalf("ConsumeTag() = %d", consumed)
	}
	if field != 1 || wireType != protowire.VarintType {
		t.Fatalf("stream close tag = (%d, %v), want (1, varint)", field, wireType)
	}
	id, consumed := protowire.ConsumeVarint(streamClose[consumed:])
	if consumed < 0 {
		t.Fatalf("ConsumeVarint() = %d", consumed)
	}
	if id != 42 {
		t.Fatalf("stream close id = %d, want 42", id)
	}
}
