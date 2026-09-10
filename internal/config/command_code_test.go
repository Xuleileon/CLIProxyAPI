package config

import "testing"

func TestCommandCodeConfigParsing(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`command-code-api-key:
 - name: ' Team '
   api-key: ' test-key '
   prefix: ' cc/ '
   models:
    - name: ' deepseek/deepseek-v4-flash '
      alias: ' flash '
 - api-key: ''
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CommandCodeKey) != 1 {
		t.Fatal("empty key not removed")
	}
	k := cfg.CommandCodeKey[0]
	if k.APIKey != "test-key" || k.Name != "Team" || k.Prefix != "cc" || k.BaseURL != DefaultCommandCodeBaseURL || k.Models[0].Alias != "flash" {
		t.Fatal("config not normalized")
	}
}
