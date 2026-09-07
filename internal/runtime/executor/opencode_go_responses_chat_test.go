package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenCodeGoResponsesFromChatCompletions(t *testing.T) {
	const request = `{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}],"max_tokens":512,"temperature":0.2,"response_format":{"type":"json_schema","json_schema":{"name":"result","schema":{"type":"object"}}}}`
	const response = `{"id":"resp_test","object":"response","status":"completed","model":"muse-spark-1.3-contributor","text":{"format":{"type":"text"}},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/responses" || !gjson.GetBytes(body, "input").IsArray() || gjson.GetBytes(body, "messages").Exists() {
			t.Errorf("Chat request was not converted to Responses: path=%s body=%s", r.URL.Path, body)
		}
		if gjson.GetBytes(body, "max_output_tokens").Int() != 512 || gjson.GetBytes(body, "text.format.type").String() != "json_schema" {
			t.Errorf("generation or structured-output parameters lost: %s", body)
		}
		if gjson.GetBytes(body, "reasoning.effort").Exists() {
			t.Errorf("adapter injected an unrequested reasoning effort: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()
	executor := NewOpenCodeGoExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: []byte(request)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(request)})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.content").String() != "OK" || gjson.GetBytes(resp.Payload, "usage.completion_tokens").Int() != 2 {
		t.Fatalf("response is not a usable Chat Completion: %s", resp.Payload)
	}
}

func TestOpenCodeGoResponsesStreamToChatCompletions(t *testing.T) {
	const request = `{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"hi"}],"stream":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_test","created_at":1,"model":"muse-spark-1.3-contributor"}}`,
			`{"type":"response.output_text.delta","delta":"OK"}`,
			`{"type":"response.completed","response":{"id":"resp_test","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		} {
			_, _ = io.WriteString(w, "data: "+event+"\n\n")
		}
	}))
	defer server.Close()
	executor := NewOpenCodeGoExecutor(&config.Config{})
	resp, err := executor.ExecuteStream(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: []byte(request)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(request)})
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	var finished bool
	for chunk := range resp.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		content.WriteString(gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String())
		finished = finished || gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String() == "stop"
	}
	if content.String() != "OK" || !finished {
		t.Fatalf("stream content=%q finished=%v", content.String(), finished)
	}
}

func TestOpenCodeGoResponsesChatToolRoundTrip(t *testing.T) {
	const request = `{"model":"muse-spark-1.3-contributor","messages":[{"role":"user","content":"check the file"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"example.txt\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file contents"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"parallel_tool_calls":false}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, `input.#(type=="function_call_output").call_id`).String() != "call_1" || gjson.GetBytes(body, `input.#(type=="function_call_output").output`).String() != "file contents" {
			t.Errorf("tool result was lost: %s", body)
		}
		if gjson.GetBytes(body, "tools.0.name").String() != "read_file" || gjson.GetBytes(body, "parallel_tool_calls").Bool() {
			t.Errorf("tool declaration or parallel setting was lost: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_2","name":"read_file","arguments":"{\"path\":\"other.txt\"}"}]}`)
	}))
	defer server.Close()
	executor := NewOpenCodeGoExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: []byte(request)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(request)})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.id").String() != "call_2" || gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("tool call response was lost: %s", resp.Payload)
	}
}
