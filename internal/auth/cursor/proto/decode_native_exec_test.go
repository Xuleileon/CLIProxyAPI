package proto

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDecodeShellStreamArgsPreservesBridgeFields(t *testing.T) {
	args := appendStringField(nil, SHA_Command, "pwd")
	args = appendStringField(args, SHA_WorkingDirectory, `D:\workspace`)
	args = appendVarintField(args, SHA_Timeout, 120000)
	args = appendStringField(args, SHA_ToolCallID, "tool-shell")

	msg, err := DecodeAgentServerMessage(wrapExecServerMessage(7, "exec-shell", ESM_ShellStreamArgs, args))
	if err != nil {
		t.Fatalf("DecodeAgentServerMessage() error = %v", err)
	}
	if msg.Type != ServerMsgExecShellStream || msg.ExecMsgId != 7 || msg.ExecId != "exec-shell" {
		t.Fatalf("decoded envelope = %#v", msg)
	}
	if msg.Command != "pwd" || msg.WorkingDirectory != `D:\workspace` || msg.Timeout != 120000 || msg.ToolCallId != "tool-shell" {
		t.Fatalf("decoded shell args = %#v", msg)
	}
}

func TestDecodeGrepArgsPreservesGlobAndToolCallID(t *testing.T) {
	args := appendStringField(nil, GA_Glob, "**/README*")
	args = appendStringField(args, GA_OutputMode, "files_with_matches")
	args = appendStringField(args, GA_ToolCallID, "tool-grep")
	args = appendVarintField(args, GA_CaseInsensitive, 1)

	msg, err := DecodeAgentServerMessage(wrapExecServerMessage(9, "exec-grep", ESM_GrepArgs, args))
	if err != nil {
		t.Fatalf("DecodeAgentServerMessage() error = %v", err)
	}
	if msg.Type != ServerMsgExecGrepArgs || msg.Glob != "**/README*" || msg.OutputMode != "files_with_matches" || msg.ToolCallId != "tool-grep" || !msg.CaseInsensitive {
		t.Fatalf("decoded grep args = %#v", msg)
	}
}

func TestDecodeExecuteHookArgsRecognizesOnlyPreCompact(t *testing.T) {
	preCompactRequest := appendStringField(nil, 1, "automatic")
	hookArgs := appendBytesField(nil, 1, preCompactRequest)

	msg, err := DecodeAgentServerMessage(wrapExecServerMessage(27, "exec-pre-compact", ESM_ExecuteHookArgs, hookArgs))
	if err != nil {
		t.Fatalf("DecodeAgentServerMessage() error = %v", err)
	}
	if msg.Type != ServerMsgExecPreCompact || msg.ExecMsgId != 27 || msg.ExecId != "exec-pre-compact" {
		t.Fatalf("decoded pre-compact hook = %#v", msg)
	}

	nonPreCompactRequest := appendBytesField(nil, 2, []byte("not-pre-compact"))
	nonPreCompactArgs := appendBytesField(nil, 1, nonPreCompactRequest)
	msg, err = DecodeAgentServerMessage(wrapExecServerMessage(28, "exec-other-hook", ESM_ExecuteHookArgs, nonPreCompactArgs))
	if err != nil {
		t.Fatalf("DecodeAgentServerMessage() error = %v", err)
	}
	if msg.Type != ServerMsgExecOther || msg.ExecFieldNumber != ESM_ExecuteHookArgs {
		t.Fatalf("decoded non-pre-compact hook = %#v", msg)
	}
}

func wrapExecServerMessage(id uint32, execID string, argsField protowire.Number, args []byte) []byte {
	var exec []byte
	exec = appendVarintField(exec, ESM_Id, uint64(id))
	exec = appendBytesField(exec, argsField, args)
	exec = appendStringField(exec, ESM_ExecId, execID)
	return appendBytesField(nil, ASM_ExecServerMessage, exec)
}

func appendStringField(dst []byte, number protowire.Number, value string) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendString(dst, value)
}

func appendBytesField(dst []byte, number protowire.Number, value []byte) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendBytes(dst, value)
}

func appendVarintField(dst []byte, number protowire.Number, value uint64) []byte {
	dst = protowire.AppendTag(dst, number, protowire.VarintType)
	return protowire.AppendVarint(dst, value)
}

func TestDecodeCursorReplySurvivesTrailingInteraction(t *testing.T) {
	heartbeat := appendBytesField(nil, ASM_InteractionUpdate, appendBytesField(nil, 13, nil))
	get := appendVarintField(nil, KSM_Id, 41)
	get = appendBytesField(get, KSM_GetBlobArgs, appendBytesField(nil, GBA_BlobId, []byte("blob")))
	set := appendVarintField(nil, KSM_Id, 42)
	setArgs := appendBytesField(nil, SBA_BlobId, []byte("blob"))
	setArgs = appendBytesField(setArgs, SBA_BlobData, []byte("value"))
	set = appendBytesField(set, KSM_SetBlobArgs, setArgs)
	cases := []struct {
		name string
		raw  []byte
		want ServerMessageType
	}{
		{"get", appendBytesField(nil, ASM_KvServerMessage, get), ServerMsgKvGetBlob},
		{"set", appendBytesField(nil, ASM_KvServerMessage, set), ServerMsgKvSetBlob},
		{"read", wrapExecServerMessage(7, "exec-read", ESM_ReadArgs, appendStringField(nil, RA_Path, "source.go")), ServerMsgExecReadArgs},
		{"precompact", wrapExecServerMessage(8, "exec-compact", ESM_ExecuteHookArgs, appendBytesField(nil, 1, appendStringField(nil, 1, "automatic"))), ServerMsgExecPreCompact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, trailing := range []bool{false, true} {
				payload := append(append([]byte(nil), tc.raw...), heartbeat...)
				if !trailing {
					payload = append(append([]byte(nil), heartbeat...), tc.raw...)
				}
				got, err := DecodeAgentServerMessage(payload)
				if err != nil {
					t.Fatal(err)
				}
				if got.Type != tc.want {
					t.Fatalf("trailing=%v: got type %v, want %v", trailing, got.Type, tc.want)
				}
			}
		})
	}
}
