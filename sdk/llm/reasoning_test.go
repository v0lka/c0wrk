package llm

import "testing"

func TestFamilyReasoningOptions(t *testing.T) {
	tests := []struct {
		family      string
		wantOptions []string
		wantDefault string
		wantOK      bool
	}{
		{"anthropic", []string{"On", "Off"}, "On", true},
		{"openai_flagship", []string{"minimal", "low", "medium", "high"}, "high", true},
		{"openai_standard", []string{"minimal", "low", "medium", "high"}, "high", true},
		{"openai_codex", []string{"minimal", "low", "medium", "high", "max"}, "max", true},
		{"google", []string{"MINIMAL", "LOW", "MEDIUM", "HIGH"}, "HIGH", true},
		{"deepseek", []string{"Off", "High", "Max"}, "Max", true},
		{"qwen", []string{"On", "Off"}, "On", true},
		{"glm", []string{"On", "Off"}, "On", true},
		// Unsupported families
		{"mistral", nil, "", false},
		{"kimi", nil, "", false},
		{"default", nil, "", false},
		{"", nil, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.family, func(t *testing.T) {
			opts, def, ok := FamilyReasoningOptions(tt.family)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if def != tt.wantDefault {
				t.Errorf("default = %q, want %q", def, tt.wantDefault)
			}
			if len(opts) != len(tt.wantOptions) {
				t.Errorf("options len = %d, want %d", len(opts), len(tt.wantOptions))
				return
			}
			for i, opt := range opts {
				if opt != tt.wantOptions[i] {
					t.Errorf("options[%d] = %q, want %q", i, opt, tt.wantOptions[i])
				}
			}
		})
	}
}
