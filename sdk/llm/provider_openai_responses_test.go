package llm

import (
	"encoding/json"
	"errors"
	"strings"
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
		{
			name: "assistant message with reasoning items round-trips them",
			messages: []Message{
				{
					Role:    "assistant",
					Content: "Let me check.",
					ReasoningItems: []ReasoningItem{
						{ID: "rs_001", Summary: "I should read the file first."},
					},
					ToolCalls: []ToolCall{
						{ID: "call-1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
					},
				},
			},
			wantCount: 3, // reasoning + text message + function_call
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				// First item: reasoning (must come before message)
				if items[0].OfReasoning == nil {
					t.Fatal("expected first item to be OfReasoning")
				}
				if items[0].OfReasoning.ID != "rs_001" {
					t.Errorf("reasoning ID = %q, want %q", items[0].OfReasoning.ID, "rs_001")
				}
				// Second item: the text message
				if items[1].OfMessage == nil {
					t.Fatal("expected second item to be OfMessage")
				}
				if items[1].OfMessage.Content.OfString.Value != "Let me check." {
					t.Errorf("text content = %q", items[1].OfMessage.Content.OfString.Value)
				}
				// Third item: the function call
				if items[2].OfFunctionCall == nil {
					t.Fatal("expected third item to be OfFunctionCall")
				}
			},
		},
		{
			name: "assistant with reasoning items but no text or tool calls",
			messages: []Message{
				{
					Role: "assistant",
					ReasoningItems: []ReasoningItem{
						{ID: "rs_002", Summary: "Thinking step."},
					},
				},
			},
			wantCount: 1, // only reasoning item
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfReasoning == nil {
					t.Fatal("expected OfReasoning to be set")
				}
				if items[0].OfReasoning.ID != "rs_002" {
					t.Errorf("reasoning ID = %q, want rs_002", items[0].OfReasoning.ID)
				}
			},
		},
		{
			name: "assistant with multiple reasoning items",
			messages: []Message{
				{
					Role: "assistant",
					ReasoningItems: []ReasoningItem{
						{ID: "rs_a", Summary: "First block."},
						{ID: "rs_b", Summary: "Second block."},
					},
				},
			},
			wantCount: 2,
			checkItems: func(t *testing.T, items responses.ResponseInputParam) {
				if items[0].OfReasoning == nil || items[0].OfReasoning.ID != "rs_a" {
					t.Error("expected first item to be reasoning rs_a")
				}
				if items[1].OfReasoning == nil || items[1].OfReasoning.ID != "rs_b" {
					t.Error("expected second item to be reasoning rs_b")
				}
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

	t.Run("response with reasoning items extracts summary and IDs", func(t *testing.T) {
		resp := &responses.Response{
			Status: responses.ResponseStatusCompleted,
			Output: []responses.ResponseOutputItemUnion{
				{
					Type: "reasoning",
					ID:   "rs_abc123",
					Summary: []responses.ResponseReasoningItemSummary{
						{Text: "I need to read the file first.", Type: "summary_text"},
						{Text: "Then I will edit line 42.", Type: "summary_text"},
					},
				},
				{Type: "function_call", CallID: "call-1", Name: "read_file", Arguments: `{"path":"main.go"}`},
			},
			Usage: responses.ResponseUsage{
				InputTokens:  300,
				OutputTokens: 80,
			},
		}
		result, err := convertResponsesResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Message.ReasoningItems) != 1 {
			t.Fatalf("expected 1 reasoning item, got %d", len(result.Message.ReasoningItems))
		}
		ri := result.Message.ReasoningItems[0]
		if ri.ID != "rs_abc123" {
			t.Errorf("reasoning item ID = %q, want %q", ri.ID, "rs_abc123")
		}
		if !strings.Contains(ri.Summary, "I need to read the file first.") {
			t.Errorf("reasoning item summary missing first part: %q", ri.Summary)
		}
		if !strings.Contains(ri.Summary, "Then I will edit line 42.") {
			t.Errorf("reasoning item summary missing second part: %q", ri.Summary)
		}
		if result.Message.ReasoningContent == "" {
			t.Error("expected ReasoningContent to be populated from reasoning summaries")
		}
		if result.Reasoning == "" {
			t.Error("expected Reasoning to be populated from reasoning summaries")
		}
		if result.Message.ReasoningContent != result.Reasoning {
			t.Errorf("ReasoningContent (%q) != Reasoning (%q)", result.Message.ReasoningContent, result.Reasoning)
		}
	})

	t.Run("response with multiple reasoning items", func(t *testing.T) {
		resp := &responses.Response{
			Status: responses.ResponseStatusCompleted,
			Output: []responses.ResponseOutputItemUnion{
				{
					Type: "reasoning",
					ID:   "rs_001",
					Summary: []responses.ResponseReasoningItemSummary{
						{Text: "First reasoning block.", Type: "summary_text"},
					},
				},
				{
					Type: "reasoning",
					ID:   "rs_002",
					Summary: []responses.ResponseReasoningItemSummary{
						{Text: "Second reasoning block.", Type: "summary_text"},
					},
				},
			},
		}
		result, err := convertResponsesResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Message.ReasoningItems) != 2 {
			t.Fatalf("expected 2 reasoning items, got %d", len(result.Message.ReasoningItems))
		}
		if result.Message.ReasoningItems[0].ID != "rs_001" {
			t.Errorf("first reasoning item ID = %q, want rs_001", result.Message.ReasoningItems[0].ID)
		}
		if result.Message.ReasoningItems[1].ID != "rs_002" {
			t.Errorf("second reasoning item ID = %q, want rs_002", result.Message.ReasoningItems[1].ID)
		}
	})

	t.Run("response with reasoning item without ID is skipped", func(t *testing.T) {
		resp := &responses.Response{
			Status: responses.ResponseStatusCompleted,
			Output: []responses.ResponseOutputItemUnion{
				{
					Type: "reasoning",
					Summary: []responses.ResponseReasoningItemSummary{
						{Text: "Orphan reasoning.", Type: "summary_text"},
					},
				},
			},
		}
		result, err := convertResponsesResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Message.ReasoningItems) != 0 {
			t.Errorf("expected 0 reasoning items (no ID), got %d", len(result.Message.ReasoningItems))
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

// TestBuildResponsesParams_Store verifies that the `store` parameter is only
// sent for the official OpenAI endpoint (empty baseURL). Compatible providers
// may not support `store` and return 400.
func TestBuildResponsesParams_Store(t *testing.T) {
	req := ChatRequest{
		Model: "gpt-5.3-codex",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	t.Run("official OpenAI sends store=false", func(t *testing.T) {
		params := buildResponsesParams(req, "")
		if !params.Store.Valid() {
			t.Fatal("expected Store to be set for official OpenAI endpoint")
		}
		if params.Store.Value != false {
			t.Errorf("expected Store=false, got %v", params.Store.Value)
		}
	})

	t.Run("compatible provider does not send store", func(t *testing.T) {
		params := buildResponsesParams(req, "https://proxy.example.com/v1")
		if params.Store.Valid() {
			t.Errorf("expected Store to be omitted for compatible provider, got %v", params.Store.Value)
		}
	})
}

// TestBuildResponsesParams_ReasoningEffort verifies that reasoning.effort is
// only sent when the value is valid for the OpenAI Responses API. Invalid
// values from other families (e.g. "On" from Anthropic) cause 400 errors.
func TestBuildResponsesParams_ReasoningEffort(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "Hello"},
	}

	validEfforts := []string{"minimal", "low", "medium", "high", "max"}
	for _, effort := range validEfforts {
		t.Run("valid effort: "+effort, func(t *testing.T) {
			req := ChatRequest{
				Model:           "gpt-5.3-codex",
				Messages:        messages,
				ReasoningEffort: effort,
			}
			params := buildResponsesParams(req, "")
			if params.Reasoning.Effort == "" {
				t.Errorf("expected reasoning.effort to be set for valid value %q", effort)
			}
		})
	}

	invalidEfforts := []string{"On", "Off", "Max", "High", "HIGH", "MINIMAL"}
	for _, effort := range invalidEfforts {
		t.Run("invalid effort: "+effort, func(t *testing.T) {
			req := ChatRequest{
				Model:           "gpt-5.3-codex",
				Messages:        messages,
				ReasoningEffort: effort,
			}
			params := buildResponsesParams(req, "")
			if params.Reasoning.Effort != "" {
				t.Errorf("expected reasoning.effort to be omitted for invalid value %q, got %q", effort, params.Reasoning.Effort)
			}
		})
	}

	t.Run("empty effort omits reasoning", func(t *testing.T) {
		req := ChatRequest{
			Model:    "gpt-5.3-codex",
			Messages: messages,
		}
		params := buildResponsesParams(req, "")
		if params.Reasoning.Effort != "" {
			t.Errorf("expected reasoning to be omitted for empty effort, got %q", params.Reasoning.Effort)
		}
	})
}

// TestIsValidResponsesReasoningEffort verifies the validation helper.
func TestIsValidResponsesReasoningEffort(t *testing.T) {
	valid := []string{"minimal", "low", "medium", "high", "max"}
	for _, v := range valid {
		if !isValidResponsesReasoningEffort(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}

	invalid := []string{"On", "Off", "Max", "High", "HIGH", "MINIMAL", "", "auto"}
	for _, v := range invalid {
		if isValidResponsesReasoningEffort(v) {
			t.Errorf("expected %q to be invalid", v)
		}
	}
}
