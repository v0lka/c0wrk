package llm

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openai/openai-go/responses"
)

func TestConvertToResponsesInput(t *testing.T) {
	tests := []struct {
		name       string
		messages   []Message
		wantCount  int
		checkItems func(t *testing.T, items responses.ResponseInputParam)
	}{
		{
			name: "user message",
			messages: []Message{
				{Role: "user", Content: "Hello"},
			},
			wantCount: 1,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				item := items[0]
				if item.OfMessage == nil {
					t.Fatal("expected OfMessage to be set")
				}
				if item.OfMessage.Role != responses.EasyInputMessageRoleUser {
					t.Errorf("role = %q, want %q", item.OfMessage.Role, responses.EasyInputMessageRoleUser)
				}
				if item.OfMessage.Content.OfString.Value != "Hello" {
					t.Errorf("content = %q, want %q", item.OfMessage.Content.OfString.Value, "Hello")
				}
			},
		},
		{
			name: "assistant message with text",
			messages: []Message{
				{Role: "assistant", Content: "I can help."},
			},
			wantCount: 1,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				item := items[0]
				if item.OfMessage == nil {
					t.Fatal("expected OfMessage to be set")
				}
				if item.OfMessage.Role != responses.EasyInputMessageRoleAssistant {
					t.Errorf("role = %q, want %q", item.OfMessage.Role, responses.EasyInputMessageRoleAssistant)
				}
				if item.OfMessage.Content.OfString.Value != "I can help." {
					t.Errorf("content = %q, want %q", item.OfMessage.Content.OfString.Value, "I can help.")
				}
			},
		},
		{
			name: "assistant message with tool calls",
			messages: []Message{
				{
					Role:    "assistant",
					Content: "Let me search.",
					ToolCalls: []ToolCall{
						{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
					},
				},
			},
			wantCount: 2, // text message + function_call
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				// First item: the text message
				if items[0].OfMessage == nil {
					t.Fatal("expected first item to be OfMessage")
				}
				if items[0].OfMessage.Content.OfString.Value != "Let me search." {
					t.Errorf("text content = %q, want %q", items[0].OfMessage.Content.OfString.Value, "Let me search.")
				}
				// Second item: the function call
				if items[1].OfFunctionCall == nil {
					t.Fatal("expected second item to be OfFunctionCall")
				}
				if items[1].OfFunctionCall.CallID != "call-1" {
					t.Errorf("call_id = %q, want %q", items[1].OfFunctionCall.CallID, "call-1")
				}
				if items[1].OfFunctionCall.Name != "search" {
					t.Errorf("name = %q, want %q", items[1].OfFunctionCall.Name, "search")
				}
				if items[1].OfFunctionCall.Arguments != `{"q":"test"}` {
					t.Errorf("arguments = %q, want %q", items[1].OfFunctionCall.Arguments, `{"q":"test"}`)
				}
			},
		},
		{
			name: "assistant with tool calls but no text",
			messages: []Message{
				{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{ID: "call-2", Name: "read_file", Input: json.RawMessage(`{"path":"/tmp"}`)},
					},
				},
			},
			wantCount: 1, // only function_call, no text message since Content is empty
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfFunctionCall == nil {
					t.Fatal("expected OfFunctionCall to be set")
				}
				if items[0].OfFunctionCall.CallID != "call-2" {
					t.Errorf("call_id = %q, want %q", items[0].OfFunctionCall.CallID, "call-2")
				}
			},
		},
		{
			name: "tool response message",
			messages: []Message{
				{Role: "tool", Content: "result data", ToolCallID: "call-1"},
			},
			wantCount: 1,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfFunctionCallOutput == nil {
					t.Fatal("expected OfFunctionCallOutput to be set")
				}
				if items[0].OfFunctionCallOutput.CallID != "call-1" {
					t.Errorf("call_id = %q, want %q", items[0].OfFunctionCallOutput.CallID, "call-1")
				}
				if items[0].OfFunctionCallOutput.Output != "result data" {
					t.Errorf("output = %q, want %q", items[0].OfFunctionCallOutput.Output, "result data")
				}
			},
		},
		{
			name: "tool response with empty content gets fallback",
			messages: []Message{
				{Role: "tool", Content: "", ToolCallID: "call-1"},
			},
			wantCount: 1,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfFunctionCallOutput == nil {
					t.Fatal("expected OfFunctionCallOutput to be set")
				}
				if items[0].OfFunctionCallOutput.Output != "(no output)" {
					t.Errorf("output = %q, want %q", items[0].OfFunctionCallOutput.Output, "(no output)")
				}
			},
		},
		{
			name: "system messages are skipped",
			messages: []Message{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "Hi"},
			},
			wantCount: 1, // system is not converted (extracted separately by buildResponsesParams)
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				// Only the user message should be present
				if items[0].OfMessage == nil {
					t.Fatal("expected OfMessage to be set")
				}
				if items[0].OfMessage.Role != responses.EasyInputMessageRoleUser {
					t.Errorf("role = %q, want user", items[0].OfMessage.Role)
				}
			},
		},
		{
			name: "mixed conversation",
			messages: []Message{
				{Role: "user", Content: "Find the bug"},
				{Role: "assistant", Content: "Let me search.", ToolCalls: []ToolCall{
					{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"bug"}`)},
				}},
				{Role: "tool", Content: "found it", ToolCallID: "call-1"},
				{Role: "assistant", Content: "I found the bug."},
			},
			wantCount: 5, // user + assistant_text + function_call + function_call_output + assistant_text
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfMessage == nil || items[0].OfMessage.Role != responses.EasyInputMessageRoleUser {
					t.Error("expected first item to be user message")
				}
				if items[1].OfMessage == nil || items[1].OfMessage.Role != responses.EasyInputMessageRoleAssistant {
					t.Error("expected second item to be assistant message")
				}
				if items[2].OfFunctionCall == nil {
					t.Error("expected third item to be function_call")
				}
				if items[3].OfFunctionCallOutput == nil {
					t.Error("expected fourth item to be function_call_output")
				}
				if items[4].OfMessage == nil || items[4].OfMessage.Role != responses.EasyInputMessageRoleAssistant {
					t.Error("expected fifth item to be assistant message")
				}
			},
		},
		{
			name:      "empty messages",
			messages:  []Message{},
			wantCount: 0,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				// nothing to check
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertToResponsesInput(tt.messages)
			if len(items) != tt.wantCount {
				t.Fatalf("got %d items, want %d", len(items), tt.wantCount)
			}
			tt.checkItems(t, items)
		})
	}
}

func TestConvertToResponsesTools(t *testing.T) {
	tests := []struct {
		name      string
		tools     []ToolDefinition
		wantCount int
		check     func(t *testing.T, result []responses.ToolUnionParam)
	}{
		{
			name: "single tool with description",
			tools: []ToolDefinition{
				{
					Name:        "search",
					Description: "Search the codebase",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
				},
			},
			wantCount: 1,
			check: func(t *testing.T, result []responses.ToolUnionParam) {
				tool := result[0]
				if tool.OfFunction == nil {
					t.Fatal("expected OfFunction to be set")
				}
				if tool.OfFunction.Name != "search" {
					t.Errorf("name = %q, want %q", tool.OfFunction.Name, "search")
				}
				if tool.OfFunction.Description.Value != "Search the codebase" {
					t.Errorf("description = %q, want %q", tool.OfFunction.Description.Value, "Search the codebase")
				}
			},
		},
		{
			name: "multiple tools",
			tools: []ToolDefinition{
				{Name: "search", Description: "Search", InputSchema: json.RawMessage(`{"type":"object"}`)},
				{Name: "read", Description: "Read file", InputSchema: json.RawMessage(`{"type":"object"}`)},
			},
			wantCount: 2,
			check: func(t *testing.T, result []responses.ToolUnionParam) {
				if result[0].OfFunction.Name != "search" {
					t.Errorf("tool 0 name = %q, want %q", result[0].OfFunction.Name, "search")
				}
				if result[1].OfFunction.Name != "read" {
					t.Errorf("tool 1 name = %q, want %q", result[1].OfFunction.Name, "read")
				}
			},
		},
		{
			name:      "empty tools",
			tools:     []ToolDefinition{},
			wantCount: 0,
			check:     func(t *testing.T, result []responses.ToolUnionParam) {},
		},
		{
			name: "tool without description",
			tools: []ToolDefinition{
				{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)},
			},
			wantCount: 1,
			check: func(t *testing.T, result []responses.ToolUnionParam) {
				if result[0].OfFunction.Name != "ping" {
					t.Errorf("name = %q, want %q", result[0].OfFunction.Name, "ping")
				}
				// Description should not be set when empty
				if result[0].OfFunction.Description.Valid() {
					t.Error("expected description to not be set for empty description")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToResponsesTools(tt.tools)
			if len(result) != tt.wantCount {
				t.Fatalf("got %d tools, want %d", len(result), tt.wantCount)
			}
			tt.check(t, result)
		})
	}
}

func TestMapResponsesStopReason(t *testing.T) {
	tests := []struct {
		name string
		resp *responses.Response
		want string
	}{
		{
			name: "completed with text output",
			resp: &responses.Response{
				Status: responses.ResponseStatusCompleted,
				Output: []responses.ResponseOutputItemUnion{
					{Type: "message"},
				},
			},
			want: "end_turn",
		},
		{
			name: "output contains function_call",
			resp: &responses.Response{
				Status: responses.ResponseStatusCompleted,
				Output: []responses.ResponseOutputItemUnion{
					{Type: "function_call", CallID: "call-1", Name: "search"},
				},
			},
			want: "tool_use",
		},
		{
			name: "incomplete with max_output_tokens",
			resp: &responses.Response{
				Status: responses.ResponseStatusIncomplete,
				IncompleteDetails: responses.ResponseIncompleteDetails{
					Reason: "max_output_tokens",
				},
			},
			want: "max_tokens",
		},
		{
			name: "incomplete with other reason",
			resp: &responses.Response{
				Status: responses.ResponseStatusIncomplete,
				IncompleteDetails: responses.ResponseIncompleteDetails{
					Reason: "content_filter",
				},
			},
			want: "end_turn",
		},
		{
			name: "failed status",
			resp: &responses.Response{
				Status: responses.ResponseStatusFailed,
			},
			want: "error",
		},
		{
			name: "cancelled status",
			resp: &responses.Response{
				Status: responses.ResponseStatusCancelled,
			},
			want: "end_turn",
		},
		{
			name: "empty status defaults to end_turn",
			resp: &responses.Response{},
			want: "end_turn",
		},
		{
			name: "function_call takes priority over incomplete status",
			resp: &responses.Response{
				Status: responses.ResponseStatusIncomplete,
				Output: []responses.ResponseOutputItemUnion{
					{Type: "function_call"},
				},
			},
			want: "tool_use",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapResponsesStopReason(tt.resp)
			if got != tt.want {
				t.Errorf("mapResponsesStopReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvertResponsesResponse(t *testing.T) {
	t.Run("nil response", func(t *testing.T) {
		_, err := convertResponsesResponse(nil)
		if err == nil {
			t.Fatal("expected error for nil response")
		}
	})

	t.Run("completed text response", func(t *testing.T) {
		resp := &responses.Response{
			Status: responses.ResponseStatusCompleted,
			Output: []responses.ResponseOutputItemUnion{
				{Type: "message"},
			},
			Usage: responses.ResponseUsage{
				InputTokens:  100,
				OutputTokens: 50,
			},
		}
		result, err := convertResponsesResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Message.Role != "assistant" {
			t.Errorf("role = %q, want %q", result.Message.Role, "assistant")
		}
		if result.StopReason != "end_turn" {
			t.Errorf("stop_reason = %q, want %q", result.StopReason, "end_turn")
		}
		if result.Usage.InputTokens != 100 {
			t.Errorf("input_tokens = %d, want %d", result.Usage.InputTokens, 100)
		}
		if result.Usage.OutputTokens != 50 {
			t.Errorf("output_tokens = %d, want %d", result.Usage.OutputTokens, 50)
		}
	})

	t.Run("response with function calls", func(t *testing.T) {
		resp := &responses.Response{
			Status: responses.ResponseStatusCompleted,
			Output: []responses.ResponseOutputItemUnion{
				{Type: "function_call", CallID: "call-1", Name: "search", Arguments: `{"q":"test"}`},
			},
			Usage: responses.ResponseUsage{
				InputTokens:  200,
				OutputTokens: 10,
			},
		}
		result, err := convertResponsesResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Message.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(result.Message.ToolCalls))
		}
		tc := result.Message.ToolCalls[0]
		if tc.ID != "call-1" {
			t.Errorf("tool call ID = %q, want %q", tc.ID, "call-1")
		}
		if tc.Name != "search" {
			t.Errorf("tool call Name = %q, want %q", tc.Name, "search")
		}
		if string(tc.Input) != `{"q":"test"}` {
			t.Errorf("tool call Input = %q, want %q", string(tc.Input), `{"q":"test"}`)
		}
		if result.StopReason != "tool_use" {
			t.Errorf("stop_reason = %q, want %q", result.StopReason, "tool_use")
		}
	})
}

func TestWrapResponsesError(t *testing.T) {
	t.Run("generic error", func(t *testing.T) {
		err := wrapResponsesError("openai", errors.New("something failed"))
		var llmErr *Error
		if !errors.As(err, &llmErr) {
			t.Fatal("expected *Error")
		}
		if llmErr.Provider != "openai" {
			t.Errorf("provider = %q, want %q", llmErr.Provider, "openai")
		}
		if llmErr.StatusCode != 0 {
			t.Errorf("status_code = %d, want 0", llmErr.StatusCode)
		}
	})

	t.Run("error message contains responses API", func(t *testing.T) {
		err := wrapResponsesError("openai", errors.New("timeout"))
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		var llmErr *Error
		if !errors.As(err, &llmErr) {
			t.Fatal("expected *Error")
		}
		// The underlying error should mention "responses API"
		if llmErr.Err == nil {
			t.Fatal("expected underlying error")
		}
	})
}

func TestNewResponsesClient(t *testing.T) {
	t.Run("default endpoint", func(t *testing.T) {
		client := newResponsesClient("test-key", "", nil)
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})

	t.Run("custom base URL", func(t *testing.T) {
		client := newResponsesClient("test-key", "https://custom.api.com/v1", nil)
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})
}
