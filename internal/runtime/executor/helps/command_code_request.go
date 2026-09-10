package helps

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// CommandCodeRequest translates Chat Completions to the CLI bridge without prompt rewriting.
func CommandCodeRequest(body []byte, model, session string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid request JSON")
	}
	root := gjson.ParseBytes(body)
	names := map[string]string{}
	for _, msg := range root.Get("messages").Array() {
		for _, tc := range msg.Get("tool_calls").Array() {
			names[tc.Get("id").String()] = tc.Get("function.name").String()
		}
	}
	messages := make([]any, 0)
	system := make([]string, 0)
	for _, msg := range root.Get("messages").Array() {
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role == "system" || role == "developer" {
			if content.Type == gjson.String {
				system = append(system, content.String())
			} else {
				for _, p := range content.Array() {
					if p.Get("type").String() != "text" {
						return nil, fmt.Errorf("unsupported system content")
					}
					system = append(system, p.Get("text").String())
				}
			}
			continue
		}
		parts := make([]any, 0)
		if role == "tool" {
			id := msg.Get("tool_call_id").String()
			name := names[id]
			if id == "" || name == "" {
				return nil, fmt.Errorf("tool result has no matching tool call")
			}
			value := content.String()
			if content.IsArray() {
				value = content.Raw
			}
			parts = append(parts, map[string]any{"type": "tool-result", "toolCallId": id, "toolName": name, "output": map[string]any{"type": "text", "value": value}})
		} else {
			if role != "user" && role != "assistant" {
				return nil, fmt.Errorf("unsupported message role %q", role)
			}
			if content.Type == gjson.String && content.String() != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content.String()})
			} else if content.IsArray() {
				for _, p := range content.Array() {
					switch p.Get("type").String() {
					case "text":
						parts = append(parts, map[string]any{"type": "text", "text": p.Get("text").String()})
					case "image_url":
						parts = append(parts, map[string]any{"type": "image", "image": p.Get("image_url.url").String()})
					default:
						return nil, fmt.Errorf("unsupported content part %q", p.Get("type").String())
					}
				}
			}
			for _, tc := range msg.Get("tool_calls").Array() {
				var input any
				args := tc.Get("function.arguments").String()
				if args == "" {
					args = "{}"
				}
				if err := json.Unmarshal([]byte(args), &input); err != nil {
					return nil, fmt.Errorf("invalid tool arguments: %w", err)
				}
				parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": tc.Get("id").String(), "toolName": tc.Get("function.name").String(), "input": input})
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": parts})
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages are required")
	}
	params := map[string]any{"model": model, "messages": messages, "stream": true}
	if len(system) > 0 {
		params["system"] = strings.Join(system, "\n\n")
	}
	for _, field := range []string{"max_tokens", "temperature", "top_p", "stop", "reasoning_effort"} {
		if v := root.Get(field); v.Exists() {
			params[field] = v.Value()
		}
	}
	if v := root.Get("max_completion_tokens"); v.Exists() {
		params["max_tokens"] = v.Value()
	}
	tools := make([]any, 0)
	for _, t := range root.Get("tools").Array() {
		if t.Get("type").String() != "function" {
			return nil, fmt.Errorf("unsupported tool type")
		}
		f := t.Get("function")
		tools = append(tools, map[string]any{"name": f.Get("name").String(), "description": f.Get("description").String(), "input_schema": f.Get("parameters").Value()})
	}
	choice := root.Get("tool_choice")
	switch choice.String() {
	case "none":
		tools = []any{}
	case "required":
		params["tool_choice"] = map[string]any{"type": "any"}
	case "auto", "":
	default:
		if choice.Get("type").String() != "function" {
			return nil, fmt.Errorf("unsupported tool_choice")
		}
		params["tool_choice"] = map[string]any{"type": "tool", "name": choice.Get("function.name").String()}
	}
	params["tools"] = tools
	return json.Marshal(map[string]any{"params": params, "threadId": session, "memory": "", "taste": "", "skills": "", "permissionMode": "standard",
		"config": map[string]any{"workingDir": "/workspace", "date": time.Now().UTC().Format("2006-01-02"), "environment": "CLIProxyAPI", "structure": []any{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{}}})
}
