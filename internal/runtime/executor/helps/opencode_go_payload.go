package helps

import (
	"bytes"
	"encoding/json"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// NormalizeOpenCodeGoRequest preserves readable Claude compaction summaries when
// switching to a backend that cannot replay Anthropic's signed compaction blocks.
func NormalizeOpenCodeGoRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	if opts.SourceFormat != sdktranslator.FormatClaude {
		return req, opts
	}
	original := req.Payload
	req.Payload = normalizeOpenCodeGoCompaction(original)
	if len(opts.OriginalRequest) > 0 {
		if bytes.Equal(opts.OriginalRequest, original) {
			opts.OriginalRequest = req.Payload
		} else {
			opts.OriginalRequest = normalizeOpenCodeGoCompaction(opts.OriginalRequest)
		}
	}
	return req, opts
}

func normalizeOpenCodeGoCompaction(payload []byte) []byte {
	found := false
	gjson.GetBytes(payload, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			found = block.Get("type").String() == "compaction"
			return !found
		})
		return !found
	})
	if !found {
		return payload
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return payload
	}
	changed := false
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		blocks, okBlocks := message["content"].([]any)
		if !okMessage || !okBlocks {
			out = append(out, item)
			continue
		}
		messageChanged := false
		content := make([]any, 0, len(blocks))
		for _, value := range blocks {
			block, okBlock := value.(map[string]any)
			if !okBlock || block["type"] != "compaction" {
				content = append(content, value)
				continue
			}
			messageChanged = true
			// Null content is a failed compaction and is a no-op in Anthropic.
			if summary, okSummary := block["content"].(string); okSummary && summary != "" {
				content = append(content, map[string]any{"type": "text", "text": summary})
			}
		}
		if messageChanged {
			changed = true
			if len(content) == 0 {
				continue
			}
			message["content"] = content
		}
		out = append(out, message)
	}
	if !changed {
		return payload
	}
	root["messages"] = out
	encoded, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return encoded
}

// StripOpenCodeGoAnthropicContent cleans protocol blocks without traversing
// opaque tool inputs, JSON schemas, or business data inside tool results.
func StripOpenCodeGoAnthropicContent(value any) any {
	blocks, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, 0, len(blocks))
	for _, value := range blocks {
		block, okBlock := value.(map[string]any)
		if !okBlock {
			out = append(out, value)
			continue
		}
		if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
			continue
		}
		delete(block, "cache_control")
		delete(block, "signature")
		if block["type"] == "tool_result" {
			block["content"] = StripOpenCodeGoAnthropicContent(block["content"])
		}
		out = append(out, block)
	}
	return out
}
