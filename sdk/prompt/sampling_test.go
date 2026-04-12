package prompt

import (
	"testing"
)

func TestDefaultSampling(t *testing.T) {
	tests := []struct {
		name                       string
		tier                       ModelTier
		wantTemp                   float64
		wantTopP                   float64
		wantTempSet                bool
		wantTopPSet                bool
		wantMaxTokensSet           bool
		wantRepetitionPenaltySet   bool
		wantStopSequences          bool
		wantRepetitionPenaltyValue float64
		wantStopSequencesValue     []string
	}{
		{
			name:                     "large tier returns correct defaults",
			tier:                     TierLarge,
			wantTempSet:              true,
			wantTemp:                 0.5,
			wantTopPSet:              true,
			wantTopP:                 0.95,
			wantMaxTokensSet:         false,
			wantRepetitionPenaltySet: false,
			wantStopSequences:        false,
		},
		{
			name:                       "small tier returns correct defaults",
			tier:                       TierSmall,
			wantTempSet:                true,
			wantTemp:                   0.2,
			wantTopPSet:                true,
			wantTopP:                   0.9,
			wantMaxTokensSet:           false,
			wantRepetitionPenaltySet:   true,
			wantRepetitionPenaltyValue: 1.1,
			wantStopSequences:          true,
			wantStopSequencesValue:     []string{"\n\n\n\n"},
		},
		{
			name:                     "unknown tier falls back to large defaults",
			tier:                     ModelTier("unknown"),
			wantTempSet:              true,
			wantTemp:                 0.5,
			wantTopPSet:              true,
			wantTopP:                 0.95,
			wantMaxTokensSet:         false,
			wantRepetitionPenaltySet: false,
			wantStopSequences:        false,
		},
		{
			name:                     "empty tier falls back to large defaults",
			tier:                     ModelTier(""),
			wantTempSet:              true,
			wantTemp:                 0.5,
			wantTopPSet:              true,
			wantTopP:                 0.95,
			wantMaxTokensSet:         false,
			wantRepetitionPenaltySet: false,
			wantStopSequences:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultSampling(tt.tier)

			// Check Temperature
			switch {
			case tt.wantTempSet && got.Temperature == nil:
				t.Error("Temperature: expected to be set, got nil")
			case tt.wantTempSet && *got.Temperature != tt.wantTemp:
				t.Errorf("Temperature: got %v, want %v", *got.Temperature, tt.wantTemp)
			case !tt.wantTempSet && got.Temperature != nil:
				t.Errorf("Temperature: expected nil, got %v", *got.Temperature)
			}

			// Check TopP
			switch {
			case tt.wantTopPSet && got.TopP == nil:
				t.Error("TopP: expected to be set, got nil")
			case tt.wantTopPSet && *got.TopP != tt.wantTopP:
				t.Errorf("TopP: got %v, want %v", *got.TopP, tt.wantTopP)
			case !tt.wantTopPSet && got.TopP != nil:
				t.Errorf("TopP: expected nil, got %v", *got.TopP)
			}

			// Check MaxTokens
			switch {
			case tt.wantMaxTokensSet && got.MaxTokens == nil:
				t.Error("MaxTokens: expected to be set, got nil")
			case !tt.wantMaxTokensSet && got.MaxTokens != nil:
				t.Errorf("MaxTokens: expected nil, got %v", *got.MaxTokens)
			}

			// Check RepetitionPenalty
			switch {
			case tt.wantRepetitionPenaltySet && got.RepetitionPenalty == nil:
				t.Error("RepetitionPenalty: expected to be set, got nil")
			case tt.wantRepetitionPenaltySet && *got.RepetitionPenalty != tt.wantRepetitionPenaltyValue:
				t.Errorf("RepetitionPenalty: got %v, want %v", *got.RepetitionPenalty, tt.wantRepetitionPenaltyValue)
			case !tt.wantRepetitionPenaltySet && got.RepetitionPenalty != nil:
				t.Errorf("RepetitionPenalty: expected nil, got %v", *got.RepetitionPenalty)
			}

			// Check StopSequences
			switch {
			case tt.wantStopSequences && got.StopSequences == nil:
				t.Error("StopSequences: expected to be set, got nil")
			case tt.wantStopSequences && len(got.StopSequences) != len(tt.wantStopSequencesValue):
				t.Errorf("StopSequences: got %d elements, want %d", len(got.StopSequences), len(tt.wantStopSequencesValue))
			case tt.wantStopSequences:
				for i, seq := range got.StopSequences {
					if seq != tt.wantStopSequencesValue[i] {
						t.Errorf("StopSequences[%d]: got %q, want %q", i, seq, tt.wantStopSequencesValue[i])
					}
				}
			case got.StopSequences != nil:
				t.Errorf("StopSequences: expected nil, got %v", got.StopSequences)
			}
		})
	}
}
