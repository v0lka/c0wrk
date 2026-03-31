package core

import (
	"context"
	"testing"

	"github.com/user/agent/internal/llm"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller (use responses and err fields)

func TestACExtractor_ExtractCodeTaskAC(t *testing.T) {
	mockResp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			Content: `[
				{"id": "ac_1", "description": "Code compiles successfully", "check_type": "programmatic", "check_cmd": "go build ./...", "step_hint": "compile"},
				{"id": "ac_2", "description": "All tests pass", "check_type": "programmatic", "check_cmd": "go test ./...", "step_hint": "test"}
			]`,
		},
	}

	mock := &mockLLMCaller{responses: []*llm.ChatResponse{mockResp}}
	extractor := NewACExtractor(mock)

	criteria, err := extractor.Extract(context.Background(), "Create a new function with tests", "code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 2 {
		t.Fatalf("expected 2 criteria, got %d", len(criteria))
	}

	// Verify first criterion
	if criteria[0].ID != "ac_1" {
		t.Errorf("expected ID 'ac_1', got %q", criteria[0].ID)
	}
	if criteria[0].CheckType != "programmatic" {
		t.Errorf("expected CheckType 'programmatic', got %q", criteria[0].CheckType)
	}
	if criteria[0].CheckCmd != "go build ./..." {
		t.Errorf("expected CheckCmd 'go build ./...', got %q", criteria[0].CheckCmd)
	}

	// Verify second criterion
	if criteria[1].ID != "ac_2" {
		t.Errorf("expected ID 'ac_2', got %q", criteria[1].ID)
	}
	if criteria[1].CheckCmd != "go test ./..." {
		t.Errorf("expected CheckCmd 'go test ./...', got %q", criteria[1].CheckCmd)
	}
}

func TestACExtractor_ExtractGeneralTaskAC(t *testing.T) {
	mockResp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			Content: `[
				{"id": "ac_1", "description": "Response is helpful and accurate", "check_type": "llm_judge", "check_cmd": "", "step_hint": "review"}
			]`,
		},
	}

	mock := &mockLLMCaller{responses: []*llm.ChatResponse{mockResp}}
	extractor := NewACExtractor(mock)

	criteria, err := extractor.Extract(context.Background(), "Explain how databases work", "general")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 criterion, got %d", len(criteria))
	}

	if criteria[0].CheckType != "llm_judge" {
		t.Errorf("expected CheckType 'llm_judge', got %q", criteria[0].CheckType)
	}
	if criteria[0].Description != "Response is helpful and accurate" {
		t.Errorf("unexpected description: %q", criteria[0].Description)
	}
}

func TestACExtractor_ExtractHandlesEmptyResponse(t *testing.T) {
	mockResp := &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: `[]`,
		},
	}

	mock := &mockLLMCaller{responses: []*llm.ChatResponse{mockResp}}
	extractor := NewACExtractor(mock)

	criteria, err := extractor.Extract(context.Background(), "Hello", "general")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 0 {
		t.Fatalf("expected 0 criteria, got %d", len(criteria))
	}
}

func TestACExtractor_ExtractHandlesJSONInCodeBlocks(t *testing.T) {
	mockResp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			Content: "```json\n" + `[
				{"id": "ac_1", "description": "Lint passes", "check_type": "programmatic", "check_cmd": "golangci-lint run", "step_hint": "lint"}
			]` + "\n```",
		},
	}

	mock := &mockLLMCaller{responses: []*llm.ChatResponse{mockResp}}
	extractor := NewACExtractor(mock)

	criteria, err := extractor.Extract(context.Background(), "Run lint on the code", "code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 criterion, got %d", len(criteria))
	}

	if criteria[0].ID != "ac_1" {
		t.Errorf("expected ID 'ac_1', got %q", criteria[0].ID)
	}
	if criteria[0].CheckCmd != "golangci-lint run" {
		t.Errorf("expected CheckCmd 'golangci-lint run', got %q", criteria[0].CheckCmd)
	}
}

func TestParseACJSON_PlainJSON(t *testing.T) {
	input := `[{"id": "ac_1", "description": "test", "check_type": "programmatic", "check_cmd": "go test", "step_hint": ""}]`

	criteria, err := parseACJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 criterion, got %d", len(criteria))
	}
}

func TestParseACJSON_EmptyString(t *testing.T) {
	criteria, err := parseACJSON("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 0 {
		t.Fatalf("expected 0 criteria, got %d", len(criteria))
	}
}

func TestParseACJSON_CodeBlockWithLanguage(t *testing.T) {
	input := "```json\n" + `[{"id": "ac_1", "description": "test", "check_type": "llm_judge", "check_cmd": "", "step_hint": ""}]` + "\n```"

	criteria, err := parseACJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 criterion, got %d", len(criteria))
	}
}
