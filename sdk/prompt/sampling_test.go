package prompt

import (
	"testing"
)

func TestDefaultSampling(t *testing.T) {
	tests := []struct {
		name       string
		family     string
		wantTemp   *float64 // nil means Temperature should be nil
		wantTopP   *float64 // nil means TopP should be nil
		wantMaxTok bool     // whether MaxTokens should be set
	}{
		{
			name:     "anthropic returns all nil (model self-selects)",
			family:   "anthropic",
			wantTemp: nil,
			wantTopP: nil,
		},
		{
			name:     "openai_flagship returns 0.3 temperature",
			family:   "openai_flagship",
			wantTemp: pfloat(0.3),
			wantTopP: nil,
		},
		{
			name:     "openai_standard returns 0.3 temperature",
			family:   "openai_standard",
			wantTemp: pfloat(0.3),
			wantTopP: nil,
		},
		{
			name:     "google returns 1.0 temperature",
			family:   "google",
			wantTemp: pfloat(1.0),
			wantTopP: nil,
		},
		{
			name:     "mistral returns 0.3 temperature",
			family:   "mistral",
			wantTemp: pfloat(0.3),
			wantTopP: nil,
		},
		{
			name:     "deepseek returns 0.0 temperature",
			family:   "deepseek",
			wantTemp: pfloat(0.0),
			wantTopP: nil,
		},
		{
			name:     "qwen returns 0.6 temperature",
			family:   "qwen",
			wantTemp: pfloat(0.6),
			wantTopP: nil,
		},
		{
			name:     "glm returns 0.2 temperature",
			family:   "glm",
			wantTemp: pfloat(0.2),
			wantTopP: nil,
		},
		{
			name:     "kimi returns all nil (server-managed)",
			family:   "kimi",
			wantTemp: nil,
			wantTopP: nil,
		},
		{
			name:     "default family returns 0.5 temp and 0.95 topP",
			family:   "default",
			wantTemp: pfloat(0.5),
			wantTopP: pfloat(0.95),
		},
		{
			name:     "unknown family falls back to default",
			family:   "unknown_provider",
			wantTemp: pfloat(0.5),
			wantTopP: pfloat(0.95),
		},
		{
			name:     "empty family falls back to default",
			family:   "",
			wantTemp: pfloat(0.5),
			wantTopP: pfloat(0.95),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultSampling(tt.family)

			// Check Temperature
			if tt.wantTemp == nil {
				if got.Temperature != nil {
					t.Errorf("Temperature: expected nil, got %v", *got.Temperature)
				}
			} else {
				if got.Temperature == nil {
					t.Errorf("Temperature: expected %v, got nil", *tt.wantTemp)
				} else if *got.Temperature != *tt.wantTemp {
					t.Errorf("Temperature: got %v, want %v", *got.Temperature, *tt.wantTemp)
				}
			}

			// Check TopP
			if tt.wantTopP == nil {
				if got.TopP != nil {
					t.Errorf("TopP: expected nil, got %v", *got.TopP)
				}
			} else {
				if got.TopP == nil {
					t.Errorf("TopP: expected %v, got nil", *tt.wantTopP)
				} else if *got.TopP != *tt.wantTopP {
					t.Errorf("TopP: got %v, want %v", *got.TopP, *tt.wantTopP)
				}
			}

			// Check MaxTokens (should never be set in current implementation)
			if tt.wantMaxTok && got.MaxTokens == nil {
				t.Error("MaxTokens: expected to be set, got nil")
			}
			if !tt.wantMaxTok && got.MaxTokens != nil {
				t.Errorf("MaxTokens: expected nil, got %v", *got.MaxTokens)
			}
		})
	}
}

// pfloat is a test helper that returns a pointer to a float64.
func pfloat(v float64) *float64 { return &v }
