package executor

import (
	"context"
	"fmt"
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

func TestOpenCodeGoResponsesFromClaude(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			const request = `{"model":"muse-spark-1.3-contributor","max_tokens":1,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"system":"Be concise.","messages":[{"role":"user","content":"Check the file"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"a.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"OK"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"tool_choice":{"type":"tool","name":"read_file","disable_parallel_tool_use":true}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/v1/responses" || !gjson.GetBytes(body, "input").IsArray() {
					t.Errorf("request was not translated: path=%s body=%s", r.URL.Path, body)
				}
				for _, field := range []string{"messages", "context_management", "max_tokens", "system", "reasoning.effort", "include", "store"} {
					if gjson.GetBytes(body, field).Exists() {
						t.Errorf("unexpected upstream field %s: %s", field, body)
					}
				}
				if gjson.GetBytes(body, "max_output_tokens").Int() != 16 || gjson.GetBytes(body, "stream").Bool() != stream {
					t.Errorf("invalid generation controls: %s", body)
				}
				if gjson.GetBytes(body, "input.0.content.0.text").String() != "Be concise." || gjson.GetBytes(body, `input.#(type=="function_call_output").call_id`).String() != "call_1" || gjson.GetBytes(body, `input.#(type=="function_call_output").output`).String() != "OK" {
					t.Errorf("system or tool history lost: %s", body)
				}
				if gjson.GetBytes(body, "tools.0.name").String() != "read_file" || gjson.GetBytes(body, "tool_choice.name").String() != "read_file" || gjson.GetBytes(body, "parallel_tool_calls").Bool() {
					t.Errorf("tool controls lost: %s", body)
				}
				const response = `{"id":"resp_test","object":"response","status":"completed","model":"muse-spark-1.3-contributor","output":[{"type":"function_call","call_id":"call_2","name":"read_file","arguments":"{\"path\":\"b.txt\"}"}],"usage":{"input_tokens":4,"output_tokens":2}}`
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []string{
					`{"type":"response.created","response":{"id":"resp_test","model":"muse-spark-1.3-contributor"}}`,
					`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"read_file","arguments":""}}`,
					`{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":0,"delta":"{\"path\":\"b.txt\"}"}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"read_file","arguments":"{\"path\":\"b.txt\"}"}}`,
					`{"type":"response.completed","response":` + response + `}`,
				} {
					_, _ = io.WriteString(w, "data: "+event+"\n\n")
				}
			}))
			defer server.Close()
			executor := NewOpenCodeGoExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "muse-spark-1.3-contributor", Payload: []byte(request)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: []byte(request)}
			if !stream {
				resp, err := executor.Execute(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), req, opts)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(resp.Payload, "type").String() != "message" || gjson.GetBytes(resp.Payload, "content.0.id").String() != "call_2" || gjson.GetBytes(resp.Payload, "content.0.input.path").String() != "b.txt" || gjson.GetBytes(resp.Payload, "stop_reason").String() != "tool_use" {
					t.Fatalf("invalid Claude response: %s", resp.Payload)
				}
				return
			}
			resp, err := executor.ExecuteStream(context.Background(), openCodeGoTestAuth(server.URL+"/v1"), req, opts)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range resp.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			for _, want := range []string{"event: message_start", "event: content_block_start", "input_json_delta", "call_2", "read_file", "tool_use", "event: message_stop"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in stream: %s", want, output.String())
				}
			}
		})
	}
}

func TestOpenCodeGoMuseMinimumOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		model string
		limit int
		want  int
	}{
		{"muse-spark-1.3-contributor", 1, 16}, {"muse-spark-1.3-contributor", 16, 16}, {"muse-spark-1.3-contributor", 512, 512}, {"muse-spark-1.3-contributor", 0, 0}, {"gpt-5.6-luna", 1, 1},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.model, tc.limit), func(t *testing.T) {
			executor := NewOpenCodeGoExecutor(&config.Config{})
			payload := []byte(fmt.Sprintf(`{"input":"hi","max_output_tokens":%d,"context_management":[{"type":"compaction","compact_threshold":10000}]}`, tc.limit))
			out, err := executor.preparePayload(payload, payload, cliproxyexecutor.Request{Model: tc.model}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, config.OpenCodeGoProtocolResponses, false)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(out, "max_output_tokens").Int() != int64(tc.want) || !gjson.GetBytes(out, "context_management").IsArray() {
				t.Fatalf("unexpected payload: %s", out)
			}
		})
	}
}
