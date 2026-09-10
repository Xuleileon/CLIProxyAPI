package registry

import "testing"

func TestOpenCodeGoCatalogProtocols(t *testing.T) {
	models := GetOpenCodeGoModels()
	if len(models) != 27 {
		t.Fatalf("model count = %d, want 27", len(models))
	}
	if got := OpenCodeGoProtocolForModel("minimax-m3"); got != "anthropic" {
		t.Fatalf("minimax protocol = %q", got)
	}
	if got := OpenCodeGoProtocolForModel("gpt-5.6-luna"); got != "responses" {
		t.Fatalf("luna protocol = %q", got)
	}
	if got := OpenCodeGoProtocolForModel("muse-spark-1.3-contributor"); got != "responses" {
		t.Fatalf("muse protocol = %q", got)
	}
	if got := OpenCodeGoProtocolForModel("deepseek-v4-flash"); got != "openai" {
		t.Fatalf("deepseek protocol = %q", got)
	}
}
