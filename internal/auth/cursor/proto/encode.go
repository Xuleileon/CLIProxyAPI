// Package proto provides protobuf encoding for Cursor's gRPC API,
// using dynamicpb with the embedded FileDescriptorProto from agent.proto.
// This mirrors the cursor-auth TS plugin's use of @bufbuild/protobuf create()+toBinary().
package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// --- Public types ---

// RunRequestParams holds all data needed to build an AgentRunRequest.
type RunRequestParams struct {
	ModelId string
	// MaxMode maps to ModelDetails.max_mode / RequestedModel.max_mode.
	// Default false: do not force Cursor Max Mode (which burns fast-request quota).
	MaxMode        bool
	SystemPrompt   string
	UserText       string
	MessageId      string
	ConversationId string
	Images         []ImageData
	Turns          []TurnData
	McpTools       []McpToolDef
	AgentMode      AgentMode
	BlobStore      map[string][]byte // hex(sha256) -> data, populated during encoding
	RawCheckpoint  []byte            // if non-nil, use as conversation_state directly (from server checkpoint)
}

type AgentMode int32

const (
	AgentModeUnspecified AgentMode = iota
	AgentModeAgent
	AgentModeAsk
)

type ImageData struct {
	MimeType string
	Data     []byte
}

type TurnData struct {
	UserText      string
	AssistantText string
}

type McpToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// --- Helper: create a dynamic message and set fields ---

func newMsg(name string) *dynamicpb.Message {
	return dynamicpb.NewMessage(Msg(name))
}

func field(msg *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	return msg.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func setStr(msg *dynamicpb.Message, name, val string) {
	if val != "" {
		msg.Set(field(msg, name), protoreflect.ValueOfString(strings.ToValidUTF8(val, "\uFFFD")))
	}
}

func setBytes(msg *dynamicpb.Message, name string, val []byte) {
	if len(val) > 0 {
		msg.Set(field(msg, name), protoreflect.ValueOfBytes(val))
	}
}

func setUint32(msg *dynamicpb.Message, name string, val uint32) {
	msg.Set(field(msg, name), protoreflect.ValueOfUint32(val))
}

func setInt32(msg *dynamicpb.Message, name string, val int32) {
	msg.Set(field(msg, name), protoreflect.ValueOfInt32(val))
}

func setInt64(msg *dynamicpb.Message, name string, val int64) {
	msg.Set(field(msg, name), protoreflect.ValueOfInt64(val))
}

func setBool(msg *dynamicpb.Message, name string, val bool) {
	msg.Set(field(msg, name), protoreflect.ValueOfBool(val))
}

func setMsg(msg *dynamicpb.Message, name string, sub *dynamicpb.Message) {
	msg.Set(field(msg, name), protoreflect.ValueOfMessage(sub.ProtoReflect()))
}

func marshal(msg *dynamicpb.Message) []byte {
	b, err := proto.Marshal(msg)
	if err != nil {
		panic("cursor proto marshal: " + err.Error())
	}
	return b
}

// --- Encode functions mirroring cursor-fetch.ts ---

// EncodeHeartbeat returns an encoded AgentClientMessage with clientHeartbeat.
// Mirrors: create(AgentClientMessageSchema, { message: { case: 'clientHeartbeat', value: create(ClientHeartbeatSchema, {}) } })
func EncodeHeartbeat() []byte {
	hb := newMsg("ClientHeartbeat")
	acm := newMsg("AgentClientMessage")
	setMsg(acm, "client_heartbeat", hb)
	return marshal(acm)
}

// EncodeRunRequest builds a full AgentClientMessage wrapping an AgentRunRequest.
// Mirrors buildCursorRequest() in cursor-fetch.ts.
// If p.RawCheckpoint is set, it is used directly as the conversation_state bytes
// (from a previous conversation_checkpoint_update), skipping manual turn construction.
func EncodeRunRequest(p *RunRequestParams) []byte {
	if p.RawCheckpoint != nil {
		return encodeRunRequestWithCheckpoint(p)
	}

	if p.BlobStore == nil {
		p.BlobStore = make(map[string][]byte)
	}

	// --- Conversation turns ---
	// Each turn is serialized as bytes (ConversationTurnStructure → bytes)
	var turnBytes [][]byte
	for _, turn := range p.Turns {
		// UserMessage for this turn
		um := newMsg("UserMessage")
		setStr(um, "text", turn.UserText)
		setStr(um, "message_id", generateId())
		umBytes := marshal(um)

		// Steps (assistant response)
		var stepBytes [][]byte
		if turn.AssistantText != "" {
			am := newMsg("AssistantMessage")
			setStr(am, "text", turn.AssistantText)
			step := newMsg("ConversationStep")
			setMsg(step, "assistant_message", am)
			stepBytes = append(stepBytes, marshal(step))
		}

		// AgentConversationTurnStructure (fields are bytes, not submessages)
		agentTurn := newMsg("AgentConversationTurnStructure")
		setBytes(agentTurn, "user_message", umBytes)
		for _, sb := range stepBytes {
			stepsField := field(agentTurn, "steps")
			list := agentTurn.Mutable(stepsField).List()
			list.Append(protoreflect.ValueOfBytes(sb))
		}

		// ConversationTurnStructure (oneof turn → agentConversationTurn)
		cts := newMsg("ConversationTurnStructure")
		setMsg(cts, "agent_conversation_turn", agentTurn)
		turnBytes = append(turnBytes, marshal(cts))
	}

	// --- System prompt blob ---
	systemJSON, _ := json.Marshal(map[string]string{"role": "system", "content": p.SystemPrompt})
	blobId := sha256Sum(systemJSON)
	p.BlobStore[hex.EncodeToString(blobId)] = systemJSON

	// --- ConversationStateStructure ---
	css := newMsg("ConversationStateStructure")
	if p.AgentMode != AgentModeUnspecified {
		setInt32(css, "mode", int32(p.AgentMode))
	}
	// rootPromptMessagesJson: repeated bytes
	rootField := field(css, "root_prompt_messages_json")
	rootList := css.Mutable(rootField).List()
	rootList.Append(protoreflect.ValueOfBytes(blobId))
	// turns: repeated bytes (field 8) + turns_old (field 2) for compatibility
	turnsField := field(css, "turns")
	turnsList := css.Mutable(turnsField).List()
	for _, tb := range turnBytes {
		turnsList.Append(protoreflect.ValueOfBytes(tb))
	}
	turnsOldField := field(css, "turns_old")
	if turnsOldField != nil {
		turnsOldList := css.Mutable(turnsOldField).List()
		for _, tb := range turnBytes {
			turnsOldList.Append(protoreflect.ValueOfBytes(tb))
		}
	}

	// --- UserMessage (current) ---
	userMessage := newMsg("UserMessage")
	setStr(userMessage, "text", p.UserText)
	setStr(userMessage, "message_id", p.MessageId)

	// Images via SelectedContext
	if len(p.Images) > 0 {
		sc := newMsg("SelectedContext")
		imgsField := field(sc, "selected_images")
		imgsList := sc.Mutable(imgsField).List()
		for _, img := range p.Images {
			si := newMsg("SelectedImage")
			setStr(si, "uuid", generateId())
			setStr(si, "mime_type", img.MimeType)
			setBytes(si, "data", img.Data)
			imgsList.Append(protoreflect.ValueOfMessage(si.ProtoReflect()))
		}
		setMsg(userMessage, "selected_context", sc)
	}

	// --- UserMessageAction ---
	uma := newMsg("UserMessageAction")
	setMsg(uma, "user_message", userMessage)

	// --- ConversationAction ---
	ca := newMsg("ConversationAction")
	setMsg(ca, "user_message_action", uma)

	// --- ModelDetails + RequestedModel (explicit max_mode; default off) ---
	md := buildModelDetails(p.ModelId, p.MaxMode)
	rm := buildRequestedModel(p.ModelId, p.MaxMode)

	// --- AgentRunRequest ---
	arr := newMsg("AgentRunRequest")
	setMsg(arr, "conversation_state", css)
	setMsg(arr, "action", ca)
	setMsg(arr, "model_details", md)
	setMsg(arr, "requested_model", rm)
	setStr(arr, "conversation_id", p.ConversationId)

	// McpTools
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		setMsg(arr, "mcp_tools", mcpTools)
	}

	// --- AgentClientMessage ---
	acm := newMsg("AgentClientMessage")
	setMsg(acm, "run_request", arr)

	return marshal(acm)
}

// buildModelDetails constructs ModelDetails with an explicit max_mode value.
// Leaving max_mode unset lets some Cursor server paths treat CLI traffic as Max Mode.
func buildModelDetails(modelID string, maxMode bool) *dynamicpb.Message {
	md := newMsg("ModelDetails")
	setStr(md, "model_id", modelID)
	setStr(md, "display_model_id", modelID)
	setStr(md, "display_name", modelID)
	setBool(md, "max_mode", maxMode)
	return md
}

// buildRequestedModel constructs RequestedModel (AgentRunRequest field 9).
func buildRequestedModel(modelID string, maxMode bool) *dynamicpb.Message {
	rm := newMsg("RequestedModel")
	setStr(rm, "model_id", modelID)
	setBool(rm, "max_mode", maxMode)
	return rm
}

// encodeRunRequestWithCheckpoint builds an AgentClientMessage using a raw checkpoint
// as conversation_state. The checkpoint bytes are embedded directly without deserialization.
func encodeRunRequestWithCheckpoint(p *RunRequestParams) []byte {
	// Build UserMessage
	userMessage := newMsg("UserMessage")
	setStr(userMessage, "text", p.UserText)
	setStr(userMessage, "message_id", p.MessageId)
	if len(p.Images) > 0 {
		sc := newMsg("SelectedContext")
		imgsField := field(sc, "selected_images")
		imgsList := sc.Mutable(imgsField).List()
		for _, img := range p.Images {
			si := newMsg("SelectedImage")
			setStr(si, "uuid", generateId())
			setStr(si, "mime_type", img.MimeType)
			setBytes(si, "data", img.Data)
			imgsList.Append(protoreflect.ValueOfMessage(si.ProtoReflect()))
		}
		setMsg(userMessage, "selected_context", sc)
	}

	// Build ConversationAction with UserMessageAction
	uma := newMsg("UserMessageAction")
	setMsg(uma, "user_message", userMessage)
	ca := newMsg("ConversationAction")
	setMsg(ca, "user_message_action", uma)
	caBytes := marshal(ca)

	// Build ModelDetails + RequestedModel (explicit max_mode)
	mdBytes := marshal(buildModelDetails(p.ModelId, p.MaxMode))
	rmBytes := marshal(buildRequestedModel(p.ModelId, p.MaxMode))

	// Build McpTools
	var mcpToolsBytes []byte
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		mcpToolsBytes = marshal(mcpTools)
	}

	// Manually assemble AgentRunRequest using protowire to embed raw checkpoint
	var arrBuf []byte
	// field 1: conversation_state = raw checkpoint bytes (length-delimited).
	// Appending mode updates the singular field without decoding the checkpoint.
	checkpoint := append([]byte(nil), p.RawCheckpoint...)
	if p.AgentMode != AgentModeUnspecified {
		checkpoint = protowire.AppendTag(checkpoint, CSS_Mode, protowire.VarintType)
		checkpoint = protowire.AppendVarint(checkpoint, uint64(p.AgentMode))
	}
	arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationState, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, checkpoint)
	// field 2: action = ConversationAction
	arrBuf = protowire.AppendTag(arrBuf, ARR_Action, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, caBytes)
	// field 3: model_details = ModelDetails
	arrBuf = protowire.AppendTag(arrBuf, ARR_ModelDetails, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, mdBytes)
	// field 4: mcp_tools = McpTools
	if len(mcpToolsBytes) > 0 {
		arrBuf = protowire.AppendTag(arrBuf, ARR_McpTools, protowire.BytesType)
		arrBuf = protowire.AppendBytes(arrBuf, mcpToolsBytes)
	}
	// field 5: conversation_id = string
	if p.ConversationId != "" {
		arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationId, protowire.BytesType)
		arrBuf = protowire.AppendString(arrBuf, p.ConversationId)
	}
	// field 9: requested_model = RequestedModel
	arrBuf = protowire.AppendTag(arrBuf, ARR_RequestedModel, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, rmBytes)

	// Wrap in AgentClientMessage field 1 (run_request)
	var acmBuf []byte
	acmBuf = protowire.AppendTag(acmBuf, ACM_RunRequest, protowire.BytesType)
	acmBuf = protowire.AppendBytes(acmBuf, arrBuf)

	log.Debugf("cursor encode: built RunRequest with checkpoint (%d bytes), total=%d bytes", len(p.RawCheckpoint), len(acmBuf))
	return acmBuf
}

// ResumeRequestParams holds data for a ResumeAction request.
type ResumeRequestParams struct {
	ModelId        string
	MaxMode        bool
	ConversationId string
	McpTools       []McpToolDef
}

// EncodeResumeRequest builds an AgentClientMessage with ResumeAction.
// Used to resume a conversation by conversation_id without re-sending full history.
func EncodeResumeRequest(p *ResumeRequestParams) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(p.McpTools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// ResumeAction
	ra := newMsg("ResumeAction")
	setMsg(ra, "request_context", rc)

	// ConversationAction with resume_action
	ca := newMsg("ConversationAction")
	setMsg(ca, "resume_action", ra)

	// ModelDetails + RequestedModel (explicit max_mode; default off)
	md := buildModelDetails(p.ModelId, p.MaxMode)
	rm := buildRequestedModel(p.ModelId, p.MaxMode)

	// AgentRunRequest — no conversation_state needed for resume
	arr := newMsg("AgentRunRequest")
	setMsg(arr, "action", ca)
	setMsg(arr, "model_details", md)
	setMsg(arr, "requested_model", rm)
	setStr(arr, "conversation_id", p.ConversationId)

	// McpTools at top level
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		setMsg(arr, "mcp_tools", mcpTools)
	}

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "run_request", arr)
	return marshal(acm)
}

// --- KV response encoders ---
// Mirrors handleKvMessage() in cursor-fetch.ts

// EncodeKvGetBlobResult responds to a getBlobArgs request.
func EncodeKvGetBlobResult(kvId uint32, blobData []byte) []byte {
	result := newMsg("GetBlobResult")
	if blobData != nil {
		setBytes(result, "blob_data", blobData)
	}

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "get_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// EncodeKvSetBlobResult responds to a setBlobArgs request.
func EncodeKvSetBlobResult(kvId uint32) []byte {
	result := newMsg("SetBlobResult")

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "set_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// --- Exec response encoders ---
// Mirrors handleExecMessage() and sendExec() in cursor-fetch.ts

// EncodeExecRequestContextResult responds to requestContextArgs with tool definitions.
func EncodeExecRequestContextResult(execMsgId uint32, execId string, tools []McpToolDef) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(tools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range tools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// RequestContextSuccess
	rcs := newMsg("RequestContextSuccess")
	setMsg(rcs, "request_context", rc)

	// RequestContextResult (oneof success)
	rcr := newMsg("RequestContextResult")
	setMsg(rcr, "success", rcs)

	return encodeExecClientMsg(execMsgId, execId, "request_context_result", rcr)
}

// EncodeExecMcpResult responds with MCP tool result.
func EncodeExecMcpResult(execMsgId uint32, execId string, content string, isError bool) []byte {
	return EncodeExecMcpResultWithImages(execMsgId, execId, content, nil, isError)
}

// EncodeExecMcpResultWithImages preserves the native MCP text and image content
// items returned by downstream clients.
func EncodeExecMcpResultWithImages(execMsgId uint32, execId, content string, images []ImageData, isError bool) []byte {
	return EncodeExecMcpResultWithContent(execMsgId, execId, content, images, nil, isError)
}

// EncodeExecMcpResultWithContent preserves text, images, and object-shaped
// structured content supported by Cursor's native MCP result protocol.
func EncodeExecMcpResultWithContent(execMsgId uint32, execId, content string, images []ImageData, structuredContent json.RawMessage, isError bool) []byte {
	success := newMsg("McpSuccess")
	contentField := field(success, "content")
	contentList := success.Mutable(contentField).List()
	if content != "" || len(images) == 0 {
		textContent := newMsg("McpTextContent")
		setStr(textContent, "text", content)
		contentItem := newMsg("McpToolResultContentItem")
		setMsg(contentItem, "text", textContent)
		contentList.Append(protoreflect.ValueOfMessage(contentItem.ProtoReflect()))
	}
	for _, image := range images {
		if len(image.Data) == 0 {
			continue
		}
		imageContent := newMsg("McpImageContent")
		setBytes(imageContent, "data", image.Data)
		setStr(imageContent, "mime_type", image.MimeType)
		contentItem := newMsg("McpToolResultContentItem")
		setMsg(contentItem, "image", imageContent)
		contentList.Append(protoreflect.ValueOfMessage(contentItem.ProtoReflect()))
	}
	if len(structuredContent) > 0 {
		var object map[string]any
		if err := json.Unmarshal(structuredContent, &object); err == nil {
			if structured, errStruct := structpb.NewStruct(object); errStruct == nil {
				if encoded, errMarshal := proto.Marshal(structured); errMarshal == nil {
					// The embedded descriptor predates McpSuccess.structured_content,
					// so preserve the current field 3 through protobuf unknown fields.
					unknown := protowire.AppendTag(nil, MCS_StructuredContent, protowire.BytesType)
					unknown = protowire.AppendBytes(unknown, encoded)
					success.SetUnknown(append(success.GetUnknown(), unknown...))
				}
			}
		}
	}
	setBool(success, "is_error", isError)

	result := newMsg("McpResult")
	setMsg(result, "success", success)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// EncodeExecMcpError responds with MCP error.
func EncodeExecMcpError(execMsgId uint32, execId string, errMsg string) []byte {
	mcpErr := newMsg("McpError")
	setStr(mcpErr, "error", errMsg)

	result := newMsg("McpResult")
	setMsg(result, "error", mcpErr)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// EncodeExecStreamClose marks an exec stream complete after its final result.
// Cursor's native client emits this control message for every completed exec.
func EncodeExecStreamClose(execMsgId uint32) []byte {
	streamClose := newMsg("ExecClientStreamClose")
	setUint32(streamClose, "id", execMsgId)

	control := newMsg("ExecClientControlMessage")
	setMsg(control, "stream_close", streamClose)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "exec_client_control_message", control)
	return marshal(acm)
}

// EncodeExecShellResult adapts a completed downstream shell tool result to the
// native Cursor shell result expected by the active Agent Run.
func EncodeExecShellResult(execMsgId uint32, execId, command, workDir, content string, isError bool) []byte {
	result := newMsg("ShellResult")
	if isError {
		failure := newMsg("ShellFailure")
		setStr(failure, "command", command)
		setStr(failure, "working_directory", workDir)
		setInt32(failure, "exit_code", 1)
		setStr(failure, "stderr", content)
		setStr(failure, "interleaved_output", content)
		setMsg(result, "failure", failure)
	} else {
		success := newMsg("ShellSuccess")
		setStr(success, "command", command)
		setStr(success, "working_directory", workDir)
		setInt32(success, "exit_code", 0)
		setStr(success, "stdout", content)
		setStr(success, "interleaved_output", content)
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "shell_result", result)
}

// EncodeExecShellStreamResult emits the native stream lifecycle for a shell
// command that was executed through a downstream tool.
func EncodeExecShellStreamResult(execMsgId uint32, execId, workDir, content string, isError bool) [][]byte {
	start := newMsg("ShellStreamStart")
	startStream := newMsg("ShellStream")
	setMsg(startStream, "start", start)

	outputStream := newMsg("ShellStream")
	if isError {
		stderr := newMsg("ShellStreamStderr")
		setStr(stderr, "data", content)
		setMsg(outputStream, "stderr", stderr)
	} else {
		stdout := newMsg("ShellStreamStdout")
		setStr(stdout, "data", content)
		setMsg(outputStream, "stdout", stdout)
	}

	exit := newMsg("ShellStreamExit")
	if isError {
		setUint32(exit, "code", 1)
	}
	setStr(exit, "cwd", workDir)
	exitStream := newMsg("ShellStream")
	setMsg(exitStream, "exit", exit)

	return [][]byte{
		encodeExecClientMsg(execMsgId, execId, "shell_stream", startStream),
		encodeExecClientMsg(execMsgId, execId, "shell_stream", outputStream),
		encodeExecClientMsg(execMsgId, execId, "shell_stream", exitStream),
	}
}

func EncodeExecReadResult(execMsgId uint32, execId, path, content string, isError bool) []byte {
	return EncodeExecReadResultWithData(execMsgId, execId, path, content, nil, isError)
}

// EncodeExecReadResultWithData uses ReadSuccess.data for binary files. Cursor's
// native read protocol models text and binary output as a oneof.
func EncodeExecReadResultWithData(execMsgId uint32, execId, path, content string, data []byte, isError bool) []byte {
	result := newMsg("ReadResult")
	if isError {
		readErr := newMsg("ReadError")
		setStr(readErr, "path", path)
		setStr(readErr, "error", content)
		setMsg(result, "error", readErr)
	} else {
		success := newMsg("ReadSuccess")
		setStr(success, "path", path)
		if len(data) > 0 {
			setBytes(success, "data", data)
			setInt64(success, "file_size", int64(len(data)))
		} else {
			setStr(success, "content", content)
			setInt32(success, "total_lines", int32(textLineCount(content)))
			setInt64(success, "file_size", int64(len(content)))
		}
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "read_result", result)
}

func EncodeExecWriteResult(execMsgId uint32, execId, path, fileText, content string, isError bool) []byte {
	result := newMsg("WriteResult")
	if isError {
		writeErr := newMsg("WriteError")
		setStr(writeErr, "path", path)
		setStr(writeErr, "error", content)
		setMsg(result, "error", writeErr)
	} else {
		success := newMsg("WriteSuccess")
		setStr(success, "path", path)
		setInt32(success, "lines_created", int32(textLineCount(fileText)))
		setInt32(success, "file_size", int32(len(fileText)))
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "write_result", result)
}

func EncodeExecDeleteResult(execMsgId uint32, execId, path, content string, isError bool) []byte {
	result := newMsg("DeleteResult")
	if isError {
		deleteErr := newMsg("DeleteError")
		setStr(deleteErr, "path", path)
		setStr(deleteErr, "error", content)
		setMsg(result, "error", deleteErr)
	} else {
		success := newMsg("DeleteSuccess")
		setStr(success, "path", path)
		setStr(success, "deleted_file", path)
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "delete_result", result)
}

func EncodeExecLsResult(execMsgId uint32, execId, path, content string, isError bool) []byte {
	result := newMsg("LsResult")
	if isError {
		lsErr := newMsg("LsError")
		setStr(lsErr, "path", path)
		setStr(lsErr, "error", content)
		setMsg(result, "error", lsErr)
	} else {
		root := newMsg("LsDirectoryTreeNode")
		setStr(root, "abs_path", path)
		files := nonEmptyLines(content)
		childrenField := field(root, "children_files")
		children := root.Mutable(childrenField).List()
		for _, name := range files {
			fileNode := newMsg("LsDirectoryTreeNode_File")
			setStr(fileNode, "name", name)
			children.Append(protoreflect.ValueOfMessage(fileNode.ProtoReflect()))
		}
		setBool(root, "children_were_processed", true)
		setInt32(root, "num_files", int32(len(files)))
		success := newMsg("LsSuccess")
		setMsg(success, "directory_tree_root", root)
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "ls_result", result)
}

func EncodeExecGrepResult(execMsgId uint32, execId, pattern, path, outputMode, content string, isError bool) []byte {
	result := newMsg("GrepResult")
	if isError {
		grepErr := newMsg("GrepError")
		setStr(grepErr, "error", content)
		setMsg(result, "error", grepErr)
		return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
	}

	union := newMsg("GrepUnionResult")
	lines := nonEmptyLines(content)
	if outputMode == "count" {
		counts := newMsg("GrepCountResult")
		countsField := field(counts, "counts")
		countsList := counts.Mutable(countsField).List()
		var total int32
		for _, line := range lines {
			fileName, count := splitTrailingCount(line)
			fileCount := newMsg("GrepFileCount")
			setStr(fileCount, "file", fileName)
			setInt32(fileCount, "count", count)
			countsList.Append(protoreflect.ValueOfMessage(fileCount.ProtoReflect()))
			total += count
		}
		setInt32(counts, "total_files", int32(len(lines)))
		setInt32(counts, "total_matches", total)
		setMsg(union, "count", counts)
	} else if outputMode == "files_with_matches" {
		files := newMsg("GrepFilesResult")
		filesField := field(files, "files")
		filesList := files.Mutable(filesField).List()
		for _, line := range lines {
			filesList.Append(protoreflect.ValueOfString(line))
		}
		setInt32(files, "total_files", int32(len(lines)))
		setMsg(union, "files", files)
	} else {
		matches := newMsg("GrepContentResult")
		matchesField := field(matches, "matches")
		matchesList := matches.Mutable(matchesField).List()
		for _, line := range lines {
			fileName, lineNumber, lineContent := splitGrepContentLine(line, path)
			fileMatch := newMsg("GrepFileMatch")
			setStr(fileMatch, "file", fileName)
			contentMatch := newMsg("GrepContentMatch")
			setInt32(contentMatch, "line_number", lineNumber)
			setStr(contentMatch, "content", lineContent)
			matchField := field(fileMatch, "matches")
			fileMatch.Mutable(matchField).List().Append(protoreflect.ValueOfMessage(contentMatch.ProtoReflect()))
			matchesList.Append(protoreflect.ValueOfMessage(fileMatch.ProtoReflect()))
		}
		setInt32(matches, "total_lines", int32(len(lines)))
		setInt32(matches, "total_matched_lines", int32(len(lines)))
		setMsg(union, "content", matches)
	}

	success := newMsg("GrepSuccess")
	setStr(success, "pattern", pattern)
	setStr(success, "path", path)
	setStr(success, "output_mode", outputMode)
	setMsg(success, "active_editor_result", union)
	setMsg(result, "success", success)
	return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
}

func EncodeExecFetchResult(execMsgId uint32, execId, url, content string, isError bool) []byte {
	result := newMsg("FetchResult")
	if isError {
		fetchErr := newMsg("FetchError")
		setStr(fetchErr, "url", url)
		setStr(fetchErr, "error", content)
		setMsg(result, "error", fetchErr)
	} else {
		success := newMsg("FetchSuccess")
		setStr(success, "url", url)
		setStr(success, "content", content)
		setInt32(success, "status_code", 200)
		setStr(success, "content_type", "text/plain")
		setMsg(result, "success", success)
	}
	return encodeExecClientMsg(execMsgId, execId, "fetch_result", result)
}

func textLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func nonEmptyLines(content string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func splitTrailingCount(line string) (string, int32) {
	separator := strings.LastIndexByte(line, ':')
	if separator <= 0 {
		return line, 0
	}
	var count int32
	if _, err := fmt.Sscan(line[separator+1:], &count); err != nil {
		return line, 0
	}
	return line[:separator], count
}

func splitGrepContentLine(line, fallbackPath string) (string, int32, string) {
	lastSeparator := strings.LastIndexByte(line, ':')
	if lastSeparator <= 0 {
		return fallbackPath, 0, line
	}
	preceding := strings.LastIndexByte(line[:lastSeparator], ':')
	if preceding <= 0 {
		return fallbackPath, 0, line
	}
	var lineNumber int32
	if _, err := fmt.Sscan(line[preceding+1:lastSeparator], &lineNumber); err != nil {
		return fallbackPath, 0, line
	}
	return line[:preceding], lineNumber, line[lastSeparator+1:]
}

// --- Rejection encoders (mirror handleExecMessage rejections) ---

func EncodeExecReadRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("ReadRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("ReadResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "read_result", result)
}

func EncodeExecShellRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("ShellResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "shell_result", result)
}

func EncodeExecShellStreamRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	stream := newMsg("ShellStream")
	setMsg(stream, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "shell_stream", stream)
}

func EncodeExecWriteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("WriteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("WriteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "write_result", result)
}

func EncodeExecDeleteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("DeleteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("DeleteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "delete_result", result)
}

func EncodeExecLsRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("LsRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("LsResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "ls_result", result)
}

func EncodeExecGrepError(execMsgId uint32, execId string, errMsg string) []byte {
	grepErr := newMsg("GrepError")
	setStr(grepErr, "error", errMsg)
	result := newMsg("GrepResult")
	setMsg(result, "error", grepErr)
	return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
}

func EncodeExecFetchError(execMsgId uint32, execId string, url, errMsg string) []byte {
	fetchErr := newMsg("FetchError")
	setStr(fetchErr, "url", url)
	setStr(fetchErr, "error", errMsg)
	result := newMsg("FetchResult")
	setMsg(result, "error", fetchErr)
	return encodeExecClientMsg(execMsgId, execId, "fetch_result", result)
}

func EncodeExecDiagnosticsResult(execMsgId uint32, execId string) []byte {
	result := newMsg("DiagnosticsResult")
	return encodeExecClientMsg(execMsgId, execId, "diagnostics_result", result)
}

func EncodeExecBackgroundShellSpawnRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("BackgroundShellSpawnResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "background_shell_spawn_result", result)
}

func EncodeExecWriteShellStdinError(execMsgId uint32, execId string, errMsg string) []byte {
	wsErr := newMsg("WriteShellStdinError")
	setStr(wsErr, "error", errMsg)
	result := newMsg("WriteShellStdinResult")
	setMsg(result, "error", wsErr)
	return encodeExecClientMsg(execMsgId, execId, "write_shell_stdin_result", result)
}

// EncodeExecPreCompactResult acknowledges the pre-compact lifecycle hook.
func EncodeExecPreCompactResult(execMsgId uint32, execId, userMessage string) []byte {
	// The embedded descriptor predates ExecuteHook*. Encode the verified wire
	// layout directly so a descriptor refresh cannot change this acknowledgement.
	var preCompact []byte // PreCompactRequestResponse
	if userMessage != "" {
		preCompact = protowire.AppendTag(preCompact, 1, protowire.BytesType)
		preCompact = protowire.AppendString(preCompact, strings.ToValidUTF8(userMessage, "\uFFFD"))
	}

	var hookResponse []byte // ExecuteHookResponse.pre_compact = 1
	hookResponse = protowire.AppendTag(hookResponse, 1, protowire.BytesType)
	hookResponse = protowire.AppendBytes(hookResponse, preCompact)

	var hookResult []byte // ExecuteHookResult.response = 1
	hookResult = protowire.AppendTag(hookResult, 1, protowire.BytesType)
	hookResult = protowire.AppendBytes(hookResult, hookResponse)

	var exec []byte // ExecClientMessage
	exec = protowire.AppendTag(exec, ECM_Id, protowire.VarintType)
	exec = protowire.AppendVarint(exec, uint64(execMsgId))
	exec = protowire.AppendTag(exec, ECM_ExecId, protowire.BytesType)
	exec = protowire.AppendString(exec, strings.ToValidUTF8(execId, "\uFFFD"))
	exec = protowire.AppendTag(exec, ECM_ExecuteHookResult, protowire.BytesType)
	exec = protowire.AppendBytes(exec, hookResult)

	var client []byte // AgentClientMessage.exec_client_message = 2
	client = protowire.AppendTag(client, ACM_ExecClientMessage, protowire.BytesType)
	return protowire.AppendBytes(client, exec)
}

// encodeExecClientMsg wraps an exec result in AgentClientMessage.
// Mirrors sendExec() in cursor-fetch.ts.
func encodeExecClientMsg(id uint32, execId string, resultFieldName string, resultMsg *dynamicpb.Message) []byte {
	ecm := newMsg("ExecClientMessage")
	setUint32(ecm, "id", id)
	// Force set exec_id even if empty - Cursor requires this field to be set
	ecm.Set(field(ecm, "exec_id"), protoreflect.ValueOfString(execId))

	// Debug: check if field exists
	fd := field(ecm, resultFieldName)
	if fd == nil {
		panic(fmt.Sprintf("field %q NOT FOUND in ExecClientMessage! Available fields: %v", resultFieldName, listFields(ecm)))
	}

	// Debug: log the actual field being set
	log.Debugf("encodeExecClientMsg: setting field %q (number=%d, kind=%s)", fd.Name(), fd.Number(), fd.Kind())

	ecm.Set(fd, protoreflect.ValueOfMessage(resultMsg.ProtoReflect()))

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "exec_client_message", ecm)
	return marshal(acm)
}

func listFields(msg *dynamicpb.Message) []string {
	var names []string
	for i := 0; i < msg.Descriptor().Fields().Len(); i++ {
		names = append(names, string(msg.Descriptor().Fields().Get(i).Name()))
	}
	return names
}

// --- Utilities ---

// jsonToProtobufValueBytes converts a JSON schema (json.RawMessage) to protobuf Value binary.
// This mirrors the TS pattern: toBinary(ValueSchema, fromJson(ValueSchema, jsonSchema))
func jsonToProtobufValueBytes(jsonData json.RawMessage) []byte {
	if len(jsonData) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(jsonData, &v); err != nil {
		return jsonData // fallback to raw JSON if parsing fails
	}
	pbVal, err := structpb.NewValue(v)
	if err != nil {
		return jsonData // fallback
	}
	b, err := proto.Marshal(pbVal)
	if err != nil {
		return jsonData // fallback
	}
	return b
}

// ProtobufValueBytesToJSON converts protobuf Value binary back to JSON.
// This mirrors the TS pattern: toJson(ValueSchema, fromBinary(ValueSchema, value))
func ProtobufValueBytesToJSON(data []byte) (interface{}, error) {
	val := &structpb.Value{}
	if err := proto.Unmarshal(data, val); err != nil {
		return nil, err
	}
	return val.AsInterface(), nil
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

var idCounter uint64

func generateId() string {
	idCounter++
	h := sha256.Sum256([]byte{byte(idCounter), byte(idCounter >> 8), byte(idCounter >> 16)})
	return hex.EncodeToString(h[:16])
}
