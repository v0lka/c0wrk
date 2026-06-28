package llm

import (
	"testing"
)

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		mapping map[string]string
		want    string
	}{
		{
			name:    "openai stop maps to end_turn",
			reason:  "stop",
			mapping: openAIStopReasonMap,
			want:    "end_turn",
		},
		{
			name:    "openai tool_calls maps to tool_use",
			reason:  "tool_calls",
			mapping: openAIStopReasonMap,
			want:    "tool_use",
		},
		{
			name:    "openai length maps to max_tokens",
			reason:  "length",
			mapping: openAIStopReasonMap,
			want:    "max_tokens",
		},
		{
			name:    "empty reason returns end_turn",
			reason:  "",
			mapping: openAIStopReasonMap,
			want:    "end_turn",
		},
		{
			name:    "unknown reason passed through",
			reason:  "content_filter",
			mapping: openAIStopReasonMap,
			want:    "content_filter",
		},
		{
			name:    "custom mapping table",
			reason:  "STOP",
			mapping: map[string]string{"STOP": "done", "SAFETY": "blocked"},
			want:    "done",
		},
		{
			name:    "nil mapping with non-empty reason passes through",
			reason:  "something",
			mapping: nil,
			want:    "something",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapStopReason(tt.reason, tt.mapping)
			if got != tt.want {
				t.Errorf("MapStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

func TestExtractSystemPrompt(t *testing.T) {
	tests := []struct {
		name          string
		messages      []Message
		wantPrompt    string
		wantFiltered  int
		wantFirstRole string
	}{
		{
			name: "no system messages",
			messages: []Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			wantPrompt:    "",
			wantFiltered:  2,
			wantFirstRole: "user",
		},
		{
			name: "single system message",
			messages: []Message{
				{Role: "system", Content: "You are helpful"},
				{Role: "user", Content: "hello"},
			},
			wantPrompt:    "You are helpful",
			wantFiltered:  1,
			wantFirstRole: "user",
		},
		{
			name: "multiple system messages concatenated",
			messages: []Message{
				{Role: "system", Content: "You are helpful"},
				{Role: "system", Content: "Be concise"},
				{Role: "user", Content: "hello"},
			},
			wantPrompt:    "You are helpful\nBe concise",
			wantFiltered:  1,
			wantFirstRole: "user",
		},
		{
			name: "system interleaved with other messages",
			messages: []Message{
				{Role: "system", Content: "First"},
				{Role: "user", Content: "hello"},
				{Role: "system", Content: "Second"},
				{Role: "assistant", Content: "hi"},
			},
			wantPrompt:    "First\nSecond",
			wantFiltered:  2,
			wantFirstRole: "user",
		},
		{
			name: "only system messages",
			messages: []Message{
				{Role: "system", Content: "Only system"},
			},
			wantPrompt:   "Only system",
			wantFiltered: 0,
		},
		{
			name:         "empty message list",
			messages:     []Message{},
			wantPrompt:   "",
			wantFiltered: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, filtered := ExtractSystemPrompt(tt.messages)
			if prompt != tt.wantPrompt {
				t.Errorf("prompt = %q, want %q", prompt, tt.wantPrompt)
			}
			if len(filtered) != tt.wantFiltered {
				t.Errorf("filtered count = %d, want %d", len(filtered), tt.wantFiltered)
			}
			if tt.wantFirstRole != "" && len(filtered) > 0 && filtered[0].Role != tt.wantFirstRole {
				t.Errorf("first filtered role = %q, want %q", filtered[0].Role, tt.wantFirstRole)
			}
		})
	}
}

func TestExtractSystemPromptParts(t *testing.T) {
	tests := []struct {
		name         string
		messages     []Message
		wantParts    []string
		wantFiltered int
	}{
		{
			name:         "no messages",
			messages:     []Message{},
			wantParts:    nil,
			wantFiltered: 0,
		},
		{
			name: "no system messages",
			messages: []Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			wantParts:    nil,
			wantFiltered: 2,
		},
		{
			name: "single system message",
			messages: []Message{
				{Role: "system", Content: "You are helpful"},
				{Role: "user", Content: "hello"},
			},
			wantParts:    []string{"You are helpful"},
			wantFiltered: 1,
		},
		{
			name: "multiple system messages become separate parts",
			messages: []Message{
				{Role: "system", Content: "Stable instructions"},
				{Role: "system", Content: "Dynamic context"},
				{Role: "user", Content: "hello"},
			},
			wantParts:    []string{"Stable instructions", "Dynamic context"},
			wantFiltered: 1,
		},
		{
			name: "empty system message content is skipped",
			messages: []Message{
				{Role: "system", Content: "First"},
				{Role: "system", Content: ""},
				{Role: "system", Content: "Third"},
				{Role: "user", Content: "hello"},
			},
			wantParts:    []string{"First", "Third"},
			wantFiltered: 1,
		},
		{
			name: "system messages interleaved with others",
			messages: []Message{
				{Role: "system", Content: "Part A"},
				{Role: "user", Content: "q1"},
				{Role: "system", Content: "Part B"},
				{Role: "assistant", Content: "a1"},
			},
			wantParts:    []string{"Part A", "Part B"},
			wantFiltered: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, filtered := ExtractSystemPromptParts(tt.messages)
			if len(parts) != len(tt.wantParts) {
				t.Fatalf("parts count = %d, want %d: %v", len(parts), len(tt.wantParts), parts)
			}
			for i := range parts {
				if parts[i] != tt.wantParts[i] {
					t.Errorf("parts[%d] = %q, want %q", i, parts[i], tt.wantParts[i])
				}
			}
			if len(filtered) != tt.wantFiltered {
				t.Errorf("filtered count = %d, want %d", len(filtered), tt.wantFiltered)
			}
			// Verify no system messages in filtered
			for _, msg := range filtered {
				if msg.Role == "system" {
					t.Errorf("filtered still contains system message: %q", msg.Content)
				}
			}
		})
	}
}
