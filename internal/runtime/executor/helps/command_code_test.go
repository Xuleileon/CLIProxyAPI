package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCommandCodeRequestPreservesToolRoundTrip(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"SYSTEM_SENTINEL"},{"role":"user","content":[{"type":"text","text":"USER_SENTINEL"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"上海\"}"}}]},{"role":"tool","tool_call_id":"call1","content":"TOOL_SENTINEL"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"tool_choice":"required","reasoning_effort":"high","max_completion_tokens":123}`)
	out, err := CommandCodeRequest(body, "test-model", "session")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"params.system": "SYSTEM_SENTINEL", "params.messages.0.content.0.text": "USER_SENTINEL", "params.messages.1.content.0.input.city": "上海", "params.messages.2.content.0.toolName": "lookup", "params.messages.2.content.0.output.value": "TOOL_SENTINEL", "params.tool_choice.type": "any", "params.reasoning_effort": "high", "params.max_tokens": "123", "threadId": "session"} {
		if got := gjson.GetBytes(out, path).String(); got != want {
			t.Errorf("%s=%q want %q", path, got, want)
		}
	}
	if strings.Contains(string(out), "CRITICAL") {
		t.Fatal("injected prompt")
	}
}

func TestCommandCodeRequestRejectsLostContent(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"tool","tool_call_id":"orphan","content":"result"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{}}]}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"f","arguments":"broken"}}]}]}`,
	} {
		if _, err := CommandCodeRequest([]byte(body), "m", "s"); err == nil {
			t.Errorf("accepted lossy request %s", body)
		}
	}
	out, err := CommandCodeRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":"none"}`), "m", "s")
	if err != nil || gjson.GetBytes(out, "params.tools.#").Int() != 0 {
		t.Fatal("tool_choice none not honored")
	}
}

func TestCommandCodeStreamToolAndUsage(t *testing.T) {
	s := NewCommandCodeStream("m")
	var chunks strings.Builder
	for _, line := range []string{
		`{"type":"start"}`,
		`{"type":"reasoning-delta","text":"think"}`,
		`{"type":"tool-input-start","id":"c1","toolName":"lookup"}`,
		`{"type":"tool-input-delta","id":"c1","delta":"{\"q\":"}`,
		`{"type":"tool-call","toolCallId":"c1","toolName":"lookup","input":{"q":"x"}}`,
		`{"type":"tool-call","toolCallId":"c1","toolName":"lookup","input":{"q":"x"}}`,
		`{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":100,"outputTokens":9}}`,
		`{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":100,"outputTokens":9,"cachedInputTokens":80,"reasoningTokens":2}}`,
	} {
		cs, err := s.Consume([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			chunks.Write(c)
		}
	}
	if strings.Count(chunks.String(), `"tool_calls":[`) != 1 {
		t.Fatalf("duplicate call %s", chunks.String())
	}
	response := s.Response()
	for path, want := range map[string]string{"choices.0.finish_reason": "tool_calls", "choices.0.message.tool_calls.0.function.arguments": `{"q":"x"}`, "usage.prompt_tokens_details.cached_tokens": "80", "usage.completion_tokens_details.reasoning_tokens": "2"} {
		if gjson.GetBytes(response, path).String() != want {
			t.Errorf("bad %s: %s", path, response)
		}
	}
}

func TestCommandCodeStreamFailures(t *testing.T) {
	for _, line := range []string{
		`{"type":"error","error":{"statusCode":503,"message":"upstream unavailable"}}`,
		`{"type":"finish","finishReason":"error"}`,
		`{"type":"tool-result","toolCallId":"a"}`,
		`{"type":"tool-call","toolCallId":"a","toolName":"f","input":"broken"}`,
		`data: [DONE]`, `not json`,
	} {
		s := NewCommandCodeStream("m")
		if _, err := s.Consume([]byte(line)); err == nil || s.Finished {
			t.Errorf("false success on %s", line)
		}
	}
}
