package proto

import (
	"encoding/hex"
	"fmt"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
)

// ServerMessageType identifies the kind of decoded server message.
type ServerMessageType int

const (
	ServerMsgUnknown             ServerMessageType = iota
	ServerMsgTextDelta                             // Text content delta
	ServerMsgThinkingDelta                         // Thinking/reasoning delta
	ServerMsgThinkingCompleted                     // Thinking completed
	ServerMsgKvGetBlob                             // Server wants a blob
	ServerMsgKvSetBlob                             // Server wants to store a blob
	ServerMsgExecRequestCtx                        // Server requests context (tools, etc.)
	ServerMsgExecMcpArgs                           // Server wants MCP tool execution
	ServerMsgExecShellArgs                         // Native shell command
	ServerMsgExecReadArgs                          // Native file read
	ServerMsgExecWriteArgs                         // Native file write
	ServerMsgExecDeleteArgs                        // Native file delete
	ServerMsgExecLsArgs                            // Native directory listing
	ServerMsgExecGrepArgs                          // Native grep search
	ServerMsgExecFetchArgs                         // Native HTTP fetch
	ServerMsgExecDiagnostics                       // Respond with empty diagnostics
	ServerMsgExecShellStream                       // Rejected: shell stream
	ServerMsgExecBgShellSpawn                      // Rejected: background shell
	ServerMsgExecWriteShellStdin                   // Rejected: write shell stdin
	ServerMsgExecPreCompact                        // Acknowledge the pre-compact hook
	ServerMsgExecOther                             // Other exec types (respond with empty)
	ServerMsgTurnEnded                             // Turn has ended (no more output)
	ServerMsgHeartbeat                             // Server heartbeat
	ServerMsgTokenDelta                            // Token usage delta
	ServerMsgCheckpoint                            // Conversation checkpoint update
)

// DecodedServerMessage holds parsed data from an AgentServerMessage.
type DecodedServerMessage struct {
	Type ServerMessageType

	// For text/thinking deltas
	Text string

	// For KV messages
	KvId     uint32
	BlobId   []byte // hex-encoded blob ID
	BlobData []byte // for setBlobArgs

	// For exec messages
	ExecMsgId uint32
	ExecId    string

	// For MCP args
	McpToolName   string
	McpToolCallId string
	McpArgs       map[string][]byte // arg name -> protobuf-encoded value

	// For native exec args
	ToolCallId       string
	Path             string
	Command          string
	WorkingDirectory string
	Url              string
	FileText         string
	FileBytes        []byte
	Ignore           []string
	Pattern          string
	Glob             string
	OutputMode       string
	FileType         string
	Timeout          int32
	ContextBefore    int32
	ContextAfter     int32
	Context          int32
	HeadLimit        int32
	CaseInsensitive  bool
	Multiline        bool
	IsBackground     bool

	// For other exec - the raw field number for building a response
	ExecFieldNumber int

	// For TokenDeltaUpdate
	TokenDelta int64

	// For conversation checkpoint update (raw bytes, not decoded)
	CheckpointData []byte
}

// DecodeAgentServerMessage parses an AgentServerMessage and returns
// a structured representation of the first meaningful message found.
func DecodeAgentServerMessage(data []byte) (*DecodedServerMessage, error) {
	msg := &DecodedServerMessage{Type: ServerMsgUnknown}

	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return msg, fmt.Errorf("invalid tag")
		}
		data = data[n:]

		switch typ {
		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return msg, fmt.Errorf("invalid bytes field %d", num)
			}
			data = data[n:]

			switch num {
			case ASM_InteractionUpdate:
				decodeInteractionUpdate(val, msg)
			case ASM_ExecServerMessage:
				decodeExecServerMessage(val, msg)
			case ASM_KvServerMessage:
				decodeKvServerMessage(val, msg)
			case ASM_ConversationCheckpoint:
				msg.Type = ServerMsgCheckpoint
				msg.CheckpointData = append([]byte(nil), val...) // copy raw bytes
				log.Debugf("DecodeAgentServerMessage: captured checkpoint %d bytes", len(val))
			}

		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return msg, fmt.Errorf("invalid varint field %d", num)
			}
			data = data[n:]

		default:
			// Skip unknown wire types
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return msg, fmt.Errorf("invalid field %d", num)
			}
			data = data[n:]
		}
	}

	return msg, nil
}

func decodeInteractionUpdate(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			previewLen := min(32, len(data))
			log.Debugf("decodeInteractionUpdate: invalid tag, remaining=%d, preview=%x, truncated=%t", len(data), data[:previewLen], len(data) > previewLen)
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				log.Debugf("decodeInteractionUpdate: invalid bytes field %d", num)
				return
			}
			data = data[n:]

			switch num {
			case IU_TextDelta:
				msg.Type = ServerMsgTextDelta
				msg.Text = decodeStringField(val, TDU_Text)
			case IU_ThinkingDelta:
				msg.Type = ServerMsgThinkingDelta
				msg.Text = decodeStringField(val, TKD_Text)
			case IU_ThinkingCompleted:
				msg.Type = ServerMsgThinkingCompleted
				log.Debugf("decodeInteractionUpdate: ThinkingCompleted")
			case 2:
				// tool_call_started - ignore but log
				log.Debugf("decodeInteractionUpdate: ToolCallStarted (ignored)")
			case 3:
				// tool_call_completed - ignore but log
				log.Debugf("decodeInteractionUpdate: ToolCallCompleted (ignored)")
			case 8:
				// token_delta - extract token count
				msg.Type = ServerMsgTokenDelta
				msg.TokenDelta = decodeVarintField(val, 1)
			case 13:
				// heartbeat from server
				msg.Type = ServerMsgHeartbeat
			case 14:
				// turn_ended - critical: model finished generating
				msg.Type = ServerMsgTurnEnded
				log.Debugf("decodeInteractionUpdate: TurnEndedUpdate - stream should end")
			case 16:
				// step_started - ignore
				log.Debugf("decodeInteractionUpdate: StepStartedUpdate (ignored)")
			case 17:
				// step_completed - ignore
				log.Debugf("decodeInteractionUpdate: StepCompletedUpdate (ignored)")
			default:
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeKvServerMessage(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		switch typ {
		case protowire.VarintType:
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == KSM_Id {
				msg.KvId = uint32(val)
			}

		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			switch num {
			case KSM_GetBlobArgs:
				msg.Type = ServerMsgKvGetBlob
				msg.BlobId = decodeBytesField(val, GBA_BlobId)
			case KSM_SetBlobArgs:
				msg.Type = ServerMsgKvSetBlob
				decodeSetBlobArgs(val, msg)
			}

		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeSetBlobArgs(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case SBA_BlobId:
				msg.BlobId = val
			case SBA_BlobData:
				msg.BlobData = val
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeExecServerMessage(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		switch typ {
		case protowire.VarintType:
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == ESM_Id {
				msg.ExecMsgId = uint32(val)
				log.Debugf("decodeExecServerMessage: ESM_Id = %d", val)
			}

		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			switch num {
			case ESM_ExecId:
				msg.ExecId = string(val)
				log.Debugf("decodeExecServerMessage: ESM_ExecId = %q", msg.ExecId)
			case ESM_RequestContextArgs:
				msg.Type = ServerMsgExecRequestCtx
			case ESM_McpArgs:
				msg.Type = ServerMsgExecMcpArgs
				decodeMcpArgs(val, msg)
			case ESM_ShellArgs:
				msg.Type = ServerMsgExecShellArgs
				decodeShellArgs(val, msg)
			case ESM_ShellStreamArgs:
				msg.Type = ServerMsgExecShellStream
				decodeShellArgs(val, msg)
			case ESM_ReadArgs:
				msg.Type = ServerMsgExecReadArgs
				decodeReadArgs(val, msg)
			case ESM_WriteArgs:
				msg.Type = ServerMsgExecWriteArgs
				decodeWriteArgs(val, msg)
			case ESM_DeleteArgs:
				msg.Type = ServerMsgExecDeleteArgs
				decodeDeleteArgs(val, msg)
			case ESM_LsArgs:
				msg.Type = ServerMsgExecLsArgs
				decodeLsArgs(val, msg)
			case ESM_GrepArgs:
				msg.Type = ServerMsgExecGrepArgs
				decodeGrepArgs(val, msg)
			case ESM_FetchArgs:
				msg.Type = ServerMsgExecFetchArgs
				decodeFetchArgs(val, msg)
			case ESM_DiagnosticsArgs:
				msg.Type = ServerMsgExecDiagnostics
			case ESM_BackgroundShellSpawn:
				msg.Type = ServerMsgExecBgShellSpawn
				decodeShellArgs(val, msg) // same structure
			case ESM_WriteShellStdinArgs:
				msg.Type = ServerMsgExecWriteShellStdin
			case ESM_ExecuteHookArgs:
				if isPreCompactExecuteHookArgs(val) {
					msg.Type = ServerMsgExecPreCompact
				} else if msg.Type == ServerMsgUnknown {
					msg.Type = ServerMsgExecOther
					msg.ExecFieldNumber = int(num)
				}
			default:
				// Unknown exec types - only set if we haven't identified the type yet
				// (other fields like span_context (19) come after the exec type field)
				if msg.Type == ServerMsgUnknown {
					msg.Type = ServerMsgExecOther
					msg.ExecFieldNumber = int(num)
				}
			}

		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

// isPreCompactExecuteHookArgs identifies ExecuteHookArgs.request.pre_compact.
// Other hooks retain the generic exec fallback path.
func isPreCompactExecuteHookArgs(data []byte) bool {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return false
		}
		data = data[n:]

		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return false
			}
			data = data[n:]
			continue
		}

		value, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return false
		}
		data = data[n:]
		if num != 1 { // ExecuteHookArgs.request
			continue
		}

		for len(value) > 0 {
			requestField, requestType, requestN := protowire.ConsumeTag(value)
			if requestN < 0 {
				return false
			}
			value = value[requestN:]
			if requestType == protowire.BytesType {
				_, requestN = protowire.ConsumeBytes(value)
			} else {
				requestN = protowire.ConsumeFieldValue(requestField, requestType, value)
			}
			if requestN < 0 {
				return false
			}
			if requestField == 1 && requestType == protowire.BytesType { // ExecuteHookRequest.pre_compact
				return true
			}
			value = value[requestN:]
		}
	}
	return false
}

func decodeMcpArgs(data []byte, msg *DecodedServerMessage) {
	msg.McpArgs = make(map[string][]byte)
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			switch num {
			case MCA_Name:
				msg.McpToolName = string(val)
			case MCA_Args:
				// Map entries are encoded as submessages with key=1, value=2
				decodeMapEntry(val, msg.McpArgs)
			case MCA_ToolCallId:
				msg.McpToolCallId = string(val)
			case MCA_ToolName:
				// ToolName takes precedence if present
				if msg.McpToolName == "" || string(val) != "" {
					msg.McpToolName = string(val)
				}
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeMapEntry(data []byte, m map[string][]byte) {
	var key string
	var value []byte
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == 1 {
				key = string(val)
			} else if num == 2 {
				value = append([]byte(nil), val...)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
	if key != "" {
		m[key] = value
	}
}

func decodeShellArgs(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case SHA_Command:
				msg.Command = string(val)
			case SHA_WorkingDirectory:
				msg.WorkingDirectory = string(val)
			case SHA_ToolCallID:
				msg.ToolCallId = string(val)
			}
		} else if typ == protowire.VarintType {
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case SHA_Timeout:
				msg.Timeout = int32(val)
			case SHA_IsBackground:
				msg.IsBackground = val != 0
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeReadArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeStringFields(data, func(num protowire.Number, val []byte) {
		switch num {
		case RA_Path:
			msg.Path = string(val)
		case RA_ToolCallID:
			msg.ToolCallId = string(val)
		}
	})
}

func decodeWriteArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeStringFields(data, func(num protowire.Number, val []byte) {
		switch num {
		case WA_Path:
			msg.Path = string(val)
		case WA_FileText:
			msg.FileText = string(val)
		case WA_ToolCallID:
			msg.ToolCallId = string(val)
		case WA_FileBytes:
			msg.FileBytes = append([]byte(nil), val...)
		}
	})
}

func decodeDeleteArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeStringFields(data, func(num protowire.Number, val []byte) {
		switch num {
		case DA_Path:
			msg.Path = string(val)
		case DA_ToolCallID:
			msg.ToolCallId = string(val)
		}
	})
}

func decodeLsArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeFields(data,
		func(num protowire.Number, val []byte) {
			switch num {
			case LA_Path:
				msg.Path = string(val)
			case LA_Ignore:
				msg.Ignore = append(msg.Ignore, string(val))
			case LA_ToolCallID:
				msg.ToolCallId = string(val)
			}
		},
		func(num protowire.Number, val uint64) {
			if num == LA_TimeoutMS {
				msg.Timeout = int32(val)
			}
		},
	)
}

func decodeGrepArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeFields(data,
		func(num protowire.Number, val []byte) {
			switch num {
			case GA_Pattern:
				msg.Pattern = string(val)
			case GA_Path:
				msg.Path = string(val)
			case GA_Glob:
				msg.Glob = string(val)
			case GA_OutputMode:
				msg.OutputMode = string(val)
			case GA_Type:
				msg.FileType = string(val)
			case GA_ToolCallID:
				msg.ToolCallId = string(val)
			}
		},
		func(num protowire.Number, val uint64) {
			switch num {
			case GA_ContextBefore:
				msg.ContextBefore = int32(val)
			case GA_ContextAfter:
				msg.ContextAfter = int32(val)
			case GA_Context:
				msg.Context = int32(val)
			case GA_HeadLimit:
				msg.HeadLimit = int32(val)
			case GA_CaseInsensitive:
				msg.CaseInsensitive = val != 0
			case GA_Multiline:
				msg.Multiline = val != 0
			}
		},
	)
}

func decodeFetchArgs(data []byte, msg *DecodedServerMessage) {
	decodeNativeStringFields(data, func(num protowire.Number, val []byte) {
		switch num {
		case FA_Url:
			msg.Url = string(val)
		case FA_ToolCallID:
			msg.ToolCallId = string(val)
		}
	})
}

func decodeNativeStringFields(data []byte, onBytes func(protowire.Number, []byte)) {
	decodeNativeFields(data, onBytes, nil)
}

func decodeNativeFields(data []byte, onBytes func(protowire.Number, []byte), onVarint func(protowire.Number, uint64)) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		switch typ {
		case protowire.BytesType:
			val, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return
			}
			data = data[consumed:]
			if onBytes != nil {
				onBytes(num, val)
			}
		case protowire.VarintType:
			val, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return
			}
			data = data[consumed:]
			if onVarint != nil {
				onVarint(num, val)
			}
		default:
			consumed := protowire.ConsumeFieldValue(num, typ, data)
			if consumed < 0 {
				return
			}
			data = data[consumed:]
		}
	}
}

// --- Helper decoders ---

// decodeStringField extracts a string from the first matching field in a submessage.
func decodeStringField(data []byte, targetField protowire.Number) string {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return ""
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return ""
			}
			data = data[n:]
			if num == targetField {
				return string(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return ""
			}
			data = data[n:]
		}
	}
	return ""
}

// decodeBytesField extracts bytes from the first matching field in a submessage.
func decodeBytesField(data []byte, targetField protowire.Number) []byte {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			if num == targetField {
				return append([]byte(nil), val...)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}
	return nil
}

// decodeVarintField extracts an int64 from the first matching varint field in a submessage.
func decodeVarintField(data []byte, targetField protowire.Number) int64 {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return 0
		}
		data = data[n:]
		if typ == protowire.VarintType {
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return 0
			}
			data = data[n:]
			if num == targetField {
				return int64(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return 0
			}
			data = data[n:]
		}
	}
	return 0
}

// BlobIdHex returns the hex string of a blob ID for use as a map key.
func BlobIdHex(blobId []byte) string {
	return hex.EncodeToString(blobId)
}
