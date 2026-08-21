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
