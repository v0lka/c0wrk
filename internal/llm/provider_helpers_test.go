package llm

import (
	"encoding/json"
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
		name           string
		messages       []Message
		wantPrompt     string
		wantFiltered   int
		wantFirstRole  string
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

func TestStreamToolCallAccumulator_HandleDelta(t *testing.T) {
	t.Run("single tool call built incrementally", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "call-1", "get_weather", "")
		acc.HandleDelta(0, "", "", `{"loc`)
		acc.HandleDelta(0, "", "", `ation":"NYC"}`)

		tc := acc.toolCalls[0]
		if tc.ID != "call-1" {
			t.Errorf("ID = %q, want %q", tc.ID, "call-1")
		}
		if tc.Name != "get_weather" {
			t.Errorf("Name = %q, want %q", tc.Name, "get_weather")
		}
		wantInput := `{"location":"NYC"}`
		if string(tc.Input) != wantInput {
			t.Errorf("Input = %q, want %q", string(tc.Input), wantInput)
		}
	})

	t.Run("multiple tool calls by different indices", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "call-1", "tool_a", `{"a":1}`)
		acc.HandleDelta(1, "call-2", "tool_b", `{"b":2}`)

		if len(acc.toolCalls) != 2 {
			t.Fatalf("expected 2 tool calls, got %d", len(acc.toolCalls))
		}
		if acc.toolCalls[0].Name != "tool_a" {
			t.Errorf("index 0 name = %q, want %q", acc.toolCalls[0].Name, "tool_a")
		}
		if acc.toolCalls[1].Name != "tool_b" {
			t.Errorf("index 1 name = %q, want %q", acc.toolCalls[1].Name, "tool_b")
		}
	})

	t.Run("argument accumulation across multiple calls", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "id", "fn", "")
		acc.HandleDelta(0, "", "", "ab")
		acc.HandleDelta(0, "", "", "cd")
		acc.HandleDelta(0, "", "", "ef")

		if string(acc.toolCalls[0].Input) != "abcdef" {
			t.Errorf("accumulated input = %q, want %q", string(acc.toolCalls[0].Input), "abcdef")
		}
	})

	t.Run("name and ID updates on later deltas", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "old-id", "old-name", "")
		acc.HandleDelta(0, "new-id", "new-name", "")

		tc := acc.toolCalls[0]
		if tc.ID != "new-id" {
			t.Errorf("ID = %q, want %q", tc.ID, "new-id")
		}
		if tc.Name != "new-name" {
			t.Errorf("Name = %q, want %q", tc.Name, "new-name")
		}
	})
}

func TestStreamToolCallAccumulator_Emit(t *testing.T) {
	t.Run("ordered emission by index", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "id-0", "first", `{}`)
		acc.HandleDelta(1, "id-1", "second", `{}`)
		acc.HandleDelta(2, "id-2", "third", `{}`)

		chunks := make(chan ChatChunk, 10)
		acc.Emit(chunks)
		close(chunks)

		var names []string
		for c := range chunks {
			if c.ToolCall != nil {
				names = append(names, c.ToolCall.Name)
			}
		}

		if len(names) != 3 {
			t.Fatalf("expected 3 emissions, got %d", len(names))
		}
		expected := []string{"first", "second", "third"}
		for i, n := range names {
			if n != expected[i] {
				t.Errorf("emission[%d] = %q, want %q", i, n, expected[i])
			}
		}
	})

	t.Run("empty accumulator emits nothing", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		chunks := make(chan ChatChunk, 10)
		acc.Emit(chunks)
		close(chunks)

		count := 0
		for range chunks {
			count++
		}
		if count != 0 {
			t.Errorf("expected 0 emissions, got %d", count)
		}
	})
}

func TestStreamToolCallAccumulator_HasToolCalls(t *testing.T) {
	t.Run("false when empty", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		if acc.HasToolCalls() {
			t.Error("expected HasToolCalls() = false for empty accumulator")
		}
	})

	t.Run("true after HandleDelta", func(t *testing.T) {
		acc := NewStreamToolCallAccumulator()
		acc.HandleDelta(0, "id", "name", "")
		if !acc.HasToolCalls() {
			t.Error("expected HasToolCalls() = true after HandleDelta")
		}
	})
}

// Verify json.RawMessage is properly handled in tool call Input field.
func TestStreamToolCallAccumulator_JSONInput(t *testing.T) {
	acc := NewStreamToolCallAccumulator()
	acc.HandleDelta(0, "call-1", "search", `{"query":"test","limit":10}`)

	var parsed map[string]interface{}
	if err := json.Unmarshal(acc.toolCalls[0].Input, &parsed); err != nil {
		t.Fatalf("failed to unmarshal accumulated input: %v", err)
	}
	if parsed["query"] != "test" {
		t.Errorf("query = %v, want %q", parsed["query"], "test")
	}
}
