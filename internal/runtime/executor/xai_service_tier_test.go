package executor

import (
	"context"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"testing"
)

func TestXAIServiceTierPolicy(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatCodex} {
		for _, enabled := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				for _, requested := range []string{"", "priority", "fast", "default"} {
					t.Run(fmt.Sprintf("%s/enabled=%t/stream=%t/request=%s", format, enabled, stream, requested), func(t *testing.T) {
						cfg := &config.Config{XAI: config.XAIConfig{PriorityProcessing: enabled}}
						override, want := "priority", "default"
						if enabled {
							override, want = "default", "priority"
						}
						// Conflicting payload rules must not bypass the user's switch.
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "grok-4.6"}}, Params: map[string]any{"service_tier": override}}}
						fields := ""
						if requested != "" {
							fields = fmt.Sprintf(`,"service_tier":%q,"speed":"fast"`, requested)
						}
						payload := []byte(`{"model":"grok-4.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"input":"hello"` + fields + `}`)
						prepared, err := NewXAIExecutor(cfg).prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload}, cliproxyexecutor.Options{SourceFormat: format}, stream)
						if err != nil {
							t.Fatal(err)
						}
						if got := gjson.GetBytes(prepared.body, "service_tier").String(); got != want {
							t.Fatalf("tier = %q, want %q", got, want)
						}
						if gjson.GetBytes(prepared.body, "speed").Exists() {
							t.Fatal("speed leaked upstream")
						}
					})
				}
			}
		}
	}
}
