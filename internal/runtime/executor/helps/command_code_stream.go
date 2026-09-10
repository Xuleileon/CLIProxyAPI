package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// CommandCodeStream owns one upstream generation. Only a finish event is success.
type CommandCodeStream struct {
	Model        string
	ID           string
	Created      int64
	Finished     bool
	finishReason string
	Usage        map[string]any
	Text         strings.Builder
	Reasoning    strings.Builder
	tools        []*commandCodeTool
}
type commandCodeTool struct {
	id, name, args string
	complete       bool
	emitted        bool
}

func NewCommandCodeStream(model string) *CommandCodeStream {
	return &CommandCodeStream{Model: model, ID: "chatcmpl-" + uuid.NewString(), Created: time.Now().Unix()}
}

func (s *CommandCodeStream) chunk(delta map[string]any, finish any, usage any) []byte {
	out := map[string]any{"id": s.ID, "object": "chat.completion.chunk", "created": s.Created, "model": s.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if usage != nil {
		out["usage"] = usage
	}
	b, _ := json.Marshal(out)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

// CommandCodeError preserves stream errors as errors rather than successful empty completions.
type CommandCodeError struct {
	Code    int
	Message string
}

func (e *CommandCodeError) Error() string   { return e.Message }
func (e *CommandCodeError) StatusCode() int { return e.Code }

func (s *CommandCodeStream) tool(id, name string) (*commandCodeTool, int, error) {
	if id == "" {
		return nil, 0, fmt.Errorf("command-code tool event missing id")
	}
	for i, t := range s.tools {
		if t.id == id {
			if name != "" {
				t.name = name
			}
			return t, i, nil
		}
	}
	t := &commandCodeTool{id: id, name: name}
	s.tools = append(s.tools, t)
	return t, len(s.tools) - 1, nil
}

func (s *CommandCodeStream) Consume(line []byte) ([][]byte, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) {
		return nil, nil
	}
	line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(line, []byte("[DONE]")) {
		if !s.Finished {
			return nil, fmt.Errorf("command-code ended without finish")
		}
		return nil, nil
	}
	if !gjson.ValidBytes(line) {
		return nil, fmt.Errorf("command-code invalid stream frame")
	}
	r := gjson.ParseBytes(line)
	kind := r.Get("type").String()
	d := r
	if nested := r.Get("data"); nested.IsObject() {
		d = nested
	}
	if kind == "error" || kind == "tool-error" {
		e := d.Get("error")
		msg := e.Get("message").String()
		if msg == "" {
			msg = e.String()
		}
		if msg == "" {
			msg = d.Get("errorText").String()
		}
		if msg == "" {
			msg = "command-code upstream stream error"
		}
		code := int(e.Get("statusCode").Int())
		if code < 400 || code > 599 {
			code = 502
		}
		return nil, &CommandCodeError{code, msg}
	}
	if s.Finished {
		return nil, nil
	}
	switch kind {
	case "text-delta", "reasoning-delta":
		text := d.Get("text").String()
		if text == "" {
			text = d.Get("textDelta").String()
		}
		if text == "" {
			text = d.Get("delta").String()
		}
		field := "content"
		if kind == "reasoning-delta" {
			field = "reasoning_content"
			s.Reasoning.WriteString(text)
		} else {
			s.Text.WriteString(text)
		}
		return [][]byte{s.chunk(map[string]any{field: text}, nil, nil)}, nil
	case "tool-input-start", "tool-input-delta", "tool-input-end":
		// AI SDK emits a validated tool-call after input chunks. Emit only that call
		// to avoid duplicate arguments or exposing malformed partial JSON.
		return nil, nil
	case "tool-call", "tool-call-delta":
		id := d.Get("toolCallId").String()
		name := d.Get("toolName").String()
		t, index, err := s.tool(id, name)
		if err != nil {
			return nil, err
		}
		if t.complete {
			return nil, nil
		}
		args := ""
		if kind == "tool-call-delta" {
			args = d.Get("argsTextDelta").String()
			if args == "" {
				args = d.Get("inputTextDelta").String()
			}
			t.args += args
		} else {
			v := d.Get("input")
			if !v.Exists() {
				v = d.Get("args")
			}
			if !v.Exists() {
				v = d.Get("arguments")
			}
			full := v.Raw
			if v.Type == gjson.String {
				full = v.String()
			}
			if full == "" {
				full = "{}"
			}
			if !json.Valid([]byte(full)) {
				return nil, fmt.Errorf("command-code returned invalid tool arguments")
			}
			if !strings.HasPrefix(full, t.args) {
				return nil, fmt.Errorf("command-code tool arguments changed after streaming")
			}
			args = strings.TrimPrefix(full, t.args)
			t.args = full
			t.complete = true
		}
		if t.name == "" {
			return nil, fmt.Errorf("command-code tool event missing name")
		}
		function := map[string]any{"arguments": args}
		call := map[string]any{"index": index, "function": function}
		if !t.emitted {
			call["id"], call["type"], function["name"] = id, "function", t.name
			t.emitted = true
		}
		return [][]byte{s.chunk(map[string]any{"tool_calls": []any{call}}, nil, nil)}, nil
	case "finish":
		reason := d.Get("finishReason").String()
		switch reason {
		case "stop":
		case "length":
		case "tool-calls", "tool-call", "tool_call":
			reason = "tool_calls"
		case "content-filter", "content_filtered":
			reason = "content_filter"
		default:
			return nil, fmt.Errorf("command-code unsuccessful finish reason %q", reason)
		}
		for _, t := range s.tools {
			if !t.complete || !json.Valid([]byte(t.args)) {
				return nil, fmt.Errorf("command-code incomplete tool call")
			}
		}
		if reason == "tool_calls" && len(s.tools) == 0 {
			return nil, fmt.Errorf("command-code finished with tool calls but returned none")
		}
		if len(s.tools) > 0 && reason == "stop" {
			reason = "tool_calls"
		}
		s.Usage = commandCodeUsage(d)
		s.Finished = true
		s.finishReason = reason
		return [][]byte{s.chunk(map[string]any{}, reason, s.Usage)}, nil
	case "start", "start-step", "finish-step", "text-start", "text-end", "reasoning-start", "reasoning-end", "provider-metadata":
		return nil, nil
	case "tool-result":
		return nil, fmt.Errorf("command-code executed a tool upstream; client tool execution required")
	default:
		return nil, fmt.Errorf("unsupported command-code stream event %q", kind)
	}
}

func commandCodeUsage(d gjson.Result) map[string]any {
	u := d.Get("totalUsage")
	if !u.Exists() {
		u = d.Get("usage")
	}
	pick := func(paths ...string) int64 {
		for _, p := range paths {
			if v := u.Get(p); v.Exists() {
				return v.Int()
			}
		}
		return 0
	}
	in := pick("inputTokens", "promptTokens", "prompt_tokens")
	out := pick("outputTokens", "completionTokens", "completion_tokens")
	return map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out, "prompt_tokens_details": map[string]any{"cached_tokens": pick("cachedInputTokens", "inputTokenDetails.cacheReadTokens", "prompt_tokens_details.cached_tokens")}, "completion_tokens_details": map[string]any{"reasoning_tokens": pick("reasoningTokens", "outputTokenDetails.reasoningTokens", "completion_tokens_details.reasoning_tokens")}}
}

func (s *CommandCodeStream) Response() []byte {
	msg := map[string]any{"role": "assistant", "content": s.Text.String()}
	if s.Reasoning.Len() > 0 {
		msg["reasoning_content"] = s.Reasoning.String()
	}
	if len(s.tools) > 0 {
		calls := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			calls = append(calls, map[string]any{"id": t.id, "type": "function", "function": map[string]any{"name": t.name, "arguments": t.args}})
		}
		msg["tool_calls"] = calls
	}
	out, _ := json.Marshal(map[string]any{"id": s.ID, "object": "chat.completion", "created": s.Created, "model": s.Model, "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": s.finishReason}}, "usage": s.Usage})
	return out
}
