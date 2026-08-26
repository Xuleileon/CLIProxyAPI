package thinking

import "testing"

func TestParseSuffix(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantName string
		wantHas  bool
		wantRaw  string
	}{
		{
			name:     "no suffix",
			model:    "claude-sonnet-5",
			wantName: "claude-sonnet-5",
		},
		{
			name:     "parenthesis level",
			model:    "claude-sonnet-5(high)",
			wantName: "claude-sonnet-5",
			wantHas:  true,
			wantRaw:  "high",
		},
		{
			name:     "parenthesis numeric",
			model:    "claude-sonnet-4-5(16384)",
			wantName: "claude-sonnet-4-5",
			wantHas:  true,
			wantRaw:  "16384",
		},
		{
			name:     "hyphen thinking level used by Claude Code",
			model:    "claude-sonnet-5-thinking-high",
			wantName: "claude-sonnet-5",
			wantHas:  true,
			wantRaw:  "high",
		},
		{
			name:     "hyphen thinking xhigh",
			model:    "claude-sonnet-5-thinking-xhigh",
			wantName: "claude-sonnet-5",
			wantHas:  true,
			wantRaw:  "xhigh",
		},
		{
			name:     "hyphen thinking none",
			model:    "claude-opus-5-thinking-none",
			wantName: "claude-opus-5",
			wantHas:  true,
			wantRaw:  "none",
		},
		{
			name:     "parenthesis wins over hyphen thinking",
			model:    "claude-sonnet-5-thinking-high(low)",
			wantName: "claude-sonnet-5-thinking-high",
			wantHas:  true,
			wantRaw:  "low",
		},
		{
			name:     "real model ending in thinking is not a suffix",
			model:    "kimi-k2-thinking",
			wantName: "kimi-k2-thinking",
		},
		{
			name:     "unknown hyphen thinking token is not a suffix",
			model:    "claude-sonnet-5-thinking-turbo",
			wantName: "claude-sonnet-5-thinking-turbo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSuffix(tt.model)
			if got.ModelName != tt.wantName || got.HasSuffix != tt.wantHas || got.RawSuffix != tt.wantRaw {
				t.Fatalf("ParseSuffix(%q) = %+v, want ModelName=%q HasSuffix=%v RawSuffix=%q",
					tt.model, got, tt.wantName, tt.wantHas, tt.wantRaw)
			}
		})
	}
}
