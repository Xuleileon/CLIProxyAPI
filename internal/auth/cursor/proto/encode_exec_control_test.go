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

func TestEncodeExecPreCompactResultAllowsEmptyResponse(t *testing.T) {
	raw := EncodeExecPreCompactResult(42, "exec-pre-compact", "")
	exec := mustFindBytesField(t, raw, ACM_ExecClientMessage)
	if id, ok := findVarintField(exec, ECM_Id); !ok || id != 42 {
		t.Fatalf("exec id = %d, found=%t; want 42", id, ok)
	}
	if execID, ok := findStringField(exec, ECM_ExecId); !ok || execID != "exec-pre-compact" {
		t.Fatalf("exec_id = %q, found=%t", execID, ok)
	}

	hookResult := mustFindBytesField(t, exec, ECM_ExecuteHookResult)
	hookResponse := mustFindBytesField(t, hookResult, 1) // ExecuteHookResult.response
	preCompact := mustFindBytesField(t, hookResponse, 1) // ExecuteHookResponse.pre_compact
	if len(preCompact) != 0 {
		t.Fatalf("empty pre-compact response = %x, want empty message", preCompact)
	}
}
