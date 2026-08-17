package config

import "testing"

func TestParseOpenCodeGoKeyAppliesDefaultsAndProtocols(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
opencode-go-api-key:
  - name: "  primary  "
    api-key: "  go-secret  "
    prefix: " team/ "
    models:
      - name: minimax-m3
        alias: mini
        protocol: messages
      - name: gpt-5.6-luna
        alias: luna
        protocol: openai-responses
  - name: "backup"
    api-key: "go-backup"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.OpenCodeGoKey) != 2 {
		t.Fatalf("key count = %d, want 2", len(cfg.OpenCodeGoKey))
	}
	entry := cfg.OpenCodeGoKey[0]
	if entry.Name != "primary" || entry.APIKey != "go-secret" || entry.BaseURL != DefaultOpenCodeGoBaseURL || entry.Prefix != "team" {
		t.Fatalf("normalized entry = %#v", entry)
	}
	if backup := cfg.OpenCodeGoKey[1]; backup.Name != "backup" || backup.APIKey != "go-backup" {
		t.Fatalf("backup entry = %#v", backup)
	}
	if entry.Models[0].Protocol != OpenCodeGoProtocolClaude || entry.Models[1].Protocol != OpenCodeGoProtocolResponses {
		t.Fatalf("protocols = %q, %q", entry.Models[0].Protocol, entry.Models[1].Protocol)
	}
}

func TestSanitizeOpenCodeGoKeysDropsInvalidEntries(t *testing.T) {
	cfg := &Config{OpenCodeGoKey: []OpenCodeGoKey{
		{APIKey: ""},
		{APIKey: "valid", Models: []OpenCodeGoModel{{Name: "bad", Protocol: "unknown"}, {Name: "good"}}},
	}}
	cfg.SanitizeOpenCodeGoKeys()
	if len(cfg.OpenCodeGoKey) != 1 || len(cfg.OpenCodeGoKey[0].Models) != 1 || cfg.OpenCodeGoKey[0].Models[0].Name != "good" {
		t.Fatalf("sanitized keys = %#v", cfg.OpenCodeGoKey)
	}
}
