package auth

import "testing"

func TestEmptyCompletionResponsesTextConfiguration(t *testing.T) {
	for _, textConfig := range []string{`{"format":{"type":"text"}}`, `{"format":{"type":"json_schema","name":"result","schema":{"type":"object"}}}`} {
		payload := []byte(`{"object":"response","status":"completed","text":` + textConfig + `,"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"output_tokens":1}}`)
		if isEmptyCompletionPayload(payload) {
			t.Fatalf("valid Responses text configuration was classified as empty: %s", textConfig)
		}
	}
}

func TestEmptyCompletionResponsesUnknownShapeIsPreserved(t *testing.T) {
	payload := []byte(`{"object":"response","status":"completed","usage":{"output_tokens":1},"delta":{"future":"shape"}}`)
	if isEmptyCompletionPayload(payload) {
		t.Fatal("unrecognized Responses field shape must not penalize credentials")
	}
}
