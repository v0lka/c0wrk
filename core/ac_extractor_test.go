package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

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

// TestEnrich_EmptyRawCriteriaReturnsReactFallback tests that Enrich calls the LLM
// to formulate a single fallback criterion when rawCriteria is empty.
func TestEnrich_EmptyRawCriteriaReturnsReactFallback(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"id": "ac_fallback_1", "description": "The code task objective has been addressed"}`,
			},
		}},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "code",
	}

	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing, "fix the bug")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 fallback criterion, got %d", len(criteria))
	}
	if criteria[0].ID != "ac_fallback_1" {
		t.Errorf("expected ID 'ac_fallback_1', got '%s'", criteria[0].ID)
	}
	if criteria[0].CheckType != "llm_judge" {
		t.Errorf("expected CheckType 'llm_judge', got '%s'", criteria[0].CheckType)
	}

	// LLM SHOULD have been called for fallback
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 LLM call for fallback, got %d", len(mock.calls))
	}
}

// TestEnrich_NilRawCriteriaReturnsResearchFallback tests that Enrich calls the LLM
// to formulate a single fallback criterion when rawCriteria is nil (research domain).
func TestEnrich_NilRawCriteriaReturnsResearchFallback(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"id": "ac_fallback_1", "description": "A comprehensive research summary must be provided"}`,
			},
		}},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "research",
	}

	criteria, err := extractor.Enrich(context.Background(), nil, routing, "research quantum computing")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return exactly 1 LLM-generated fallback criterion
	if len(criteria) != 1 {
		t.Fatalf("expected 1 fallback criterion for research domain, got %d", len(criteria))
	}
	if criteria[0].ID != "ac_fallback_1" {
		t.Errorf("expected ID 'ac_fallback_1', got '%s'", criteria[0].ID)
	}
	if criteria[0].CheckType != "llm_judge" {
		t.Errorf("expected CheckType 'llm_judge', got '%s'", criteria[0].CheckType)
	}

	// LLM SHOULD have been called
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 LLM call for fallback, got %d", len(mock.calls))
	}
}

// TestEnrich_EmptyRawCriteriaReturnsFallbackForGeneral tests that Enrich calls the LLM
// to formulate a single fallback criterion for general domain.
func TestEnrich_EmptyRawCriteriaReturnsFallbackForGeneral(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"id": "ac_fallback_1", "description": "The general request must be fulfilled"}`,
			},
		}},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "general",
	}

	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing, "hello world")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return exactly 1 LLM-generated fallback criterion
	if len(criteria) != 1 {
		t.Fatalf("expected 1 fallback criterion for general domain, got %d", len(criteria))
	}

	// LLM SHOULD have been called
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 LLM call for fallback, got %d", len(mock.calls))
	}
}

// TestEnrich_NonEmptyRawCriteriaCallsLLM tests that Enrich calls the LLM when
// rawCriteria has items (existing behavior preserved).
func TestEnrich_NonEmptyRawCriteriaCallsLLM(t *testing.T) {
	// Mock that returns enriched criteria
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `[{"id":"ac_1","description":"enriched criterion","check_type":"llm_judge","check_cmd":"","step_hint":""}]`,
			},
		}},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "code",
	}

	rawCriteria := []RawCriterion{
		{ID: "rc_1", Description: "raw criterion", Nature: "objective", Weight: "must"},
	}

	criteria, err := extractor.Enrich(context.Background(), rawCriteria, routing, "enrich this")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return enriched criteria from LLM
	if len(criteria) != 1 {
		t.Fatalf("expected 1 criterion, got %d", len(criteria))
	}
	if criteria[0].ID != "ac_1" {
		t.Errorf("expected ID 'ac_1', got '%s'", criteria[0].ID)
	}
	if criteria[0].Description != "enriched criterion" {
		t.Errorf("expected description 'enriched criterion', got '%s'", criteria[0].Description)
	}

	// LLM SHOULD have been called
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(mock.calls))
	}
}

// TestEnrich_GeneralDomainAlsoGetsSingleFallback tests that general domain
// gets exactly 1 LLM-generated fallback criterion.
func TestEnrich_GeneralDomainAlsoGetsSingleFallback(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"id": "ac_fallback_1", "description": "The general question must be answered"}`,
			},
		}},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "general",
	}

	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing, "what is Go?")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	if len(criteria) != 1 {
		t.Fatalf("expected 1 fallback criterion for general domain, got %d", len(criteria))
	}
	if criteria[0].ID != "ac_fallback_1" {
		t.Errorf("expected ID 'ac_fallback_1', got '%s'", criteria[0].ID)
	}
}

// TestEnrich_WorkspacePathIncludedInContext verifies that when workspace path is
// set in context, the enricher user message includes a "Workspace:" line.
func TestEnrich_WorkspacePathIncludedInContext(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `[{"id":"ac_1","description":"enriched","check_type":"llm_judge","check_cmd":"","step_hint":""}]`,
				},
			}, nil
		},
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{Domain: "code"}
	rawCriteria := []RawCriterion{
		{ID: "rc_1", Description: "raw criterion", Nature: "objective", Weight: "must"},
	}

	// With workspace path
	ctx := tools.WithWorkspacePath(context.Background(), "/ws/path")
	_, err := extractor.Enrich(ctx, rawCriteria, routing, "enrich with workspace")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	userMsg := capturedRequest.Messages[1].Content
	if !strings.Contains(userMsg, "Workspace: /ws/path") {
		t.Errorf("expected user message to contain 'Workspace: /ws/path', got:\n%s", userMsg)
	}

	// Without workspace path
	_, err = extractor.Enrich(context.Background(), rawCriteria, routing, "enrich without workspace")
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	userMsgNoWS := capturedRequest.Messages[1].Content
	if strings.Contains(userMsgNoWS, "Workspace:") {
		t.Errorf("expected user message to NOT contain 'Workspace:' when no workspace path, got:\n%s", userMsgNoWS)
	}
}

// TestEnrich_FallbackLLMFailureReturnsError tests that when raw criteria are empty
// and the fallback LLM call fails, Enrich returns an error.
func TestEnrich_FallbackLLMFailureReturnsError(t *testing.T) {
	mock := &mockLLMCaller{
		err: errors.New("LLM unavailable"),
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Domain: "code",
	}

	_, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing, "fix the bug")
	if err == nil {
		t.Fatal("expected error when fallback LLM call fails, got nil")
	}
	if !strings.Contains(err.Error(), "fallback AC LLM call failed") {
		t.Errorf("expected error to contain 'fallback AC LLM call failed', got: %v", err)
	}
}

func TestParseRawACJSON(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "empty string",
			content:   "",
			wantCount: 0,
		},
		{
			name:      "empty array",
			content:   "[]",
			wantCount: 0,
		},
		{
			name:      "valid JSON array",
			content:   `[{"id":"rc_1","description":"tests pass","nature":"objective","implicit":false,"weight":"must"}]`,
			wantCount: 1,
		},
		{
			name:      "markdown code block",
			content:   "```json\n[{\"id\":\"rc_1\",\"description\":\"test\",\"nature\":\"objective\",\"implicit\":false,\"weight\":\"must\"}]\n```",
			wantCount: 1,
		},
		{
			name:    "code block without array bracket",
			content: "```\nsome text\n```",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			content: `{not valid json}`,
			wantErr: true,
		},
		{
			name:      "whitespace-padded content",
			content:   "  \n  []  \n  ",
			wantCount: 0,
		},
		{
			name:      "multiple criteria",
			content:   `[{"id":"rc_1","description":"a"},{"id":"rc_2","description":"b"}]`,
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseRawACJSON(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tt.wantCount {
				t.Errorf("got %d criteria, want %d", len(result), tt.wantCount)
			}
		})
	}
}
