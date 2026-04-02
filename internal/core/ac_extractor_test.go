package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
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

// TestEnrich_EmptyRawCriteriaReturnsReactFallback tests that Enrich returns fallback
// criteria for react mode when rawCriteria is empty.
func TestEnrich_EmptyRawCriteriaReturnsReactFallback(t *testing.T) {
	// Use a mock that would fail if called - proving early return works
	mock := &mockLLMCaller{
		err: errors.New("LLM should not be called"),
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Mode:   "react",
		Domain: "code",
	}

	// Empty slice should trigger fallback
	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return 1 fallback criterion for react/code
	if len(criteria) != 1 {
		t.Fatalf("expected 1 fallback criterion, got %d", len(criteria))
	}
	if criteria[0].ID != "ac_fallback_1" {
		t.Errorf("expected ID 'ac_fallback_1', got '%s'", criteria[0].ID)
	}
	if criteria[0].CheckType != "llm_judge" {
		t.Errorf("expected CheckType 'llm_judge', got '%s'", criteria[0].CheckType)
	}

	// LLM should NOT have been called
	if len(mock.calls) != 0 {
		t.Error("LLM should not have been called for empty rawCriteria")
	}
}

// TestEnrich_NilRawCriteriaReturnsResearchFallback tests that Enrich returns fallback
// criteria including Markdown for research domain when rawCriteria is nil.
func TestEnrich_NilRawCriteriaReturnsResearchFallback(t *testing.T) {
	// Use a mock that would fail if called
	mock := &mockLLMCaller{
		err: errors.New("LLM should not be called"),
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Mode:   "react",
		Domain: "research",
	}

	// nil should trigger fallback (len(nil) == 0)
	criteria, err := extractor.Enrich(context.Background(), nil, routing)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return 2 fallback criteria for react/research (includes Markdown criterion)
	if len(criteria) != 2 {
		t.Fatalf("expected 2 fallback criteria for research domain, got %d", len(criteria))
	}

	// Verify first criterion
	if criteria[0].ID != "ac_fallback_1" {
		t.Errorf("expected first ID 'ac_fallback_1', got '%s'", criteria[0].ID)
	}

	// Verify second criterion (Markdown formatting)
	if criteria[1].ID != "ac_fallback_2" {
		t.Errorf("expected second ID 'ac_fallback_2', got '%s'", criteria[1].ID)
	}
	if !strings.Contains(criteria[1].Description, "Markdown") {
		t.Errorf("expected second criterion to mention 'Markdown', got '%s'", criteria[1].Description)
	}

	// LLM should NOT have been called
	if len(mock.calls) != 0 {
		t.Error("LLM should not have been called for nil rawCriteria")
	}
}

// TestEnrich_EmptyRawCriteriaReturnsEmptyForDirectMode tests that Enrich returns
// empty criteria for direct mode when rawCriteria is empty.
func TestEnrich_EmptyRawCriteriaReturnsEmptyForDirectMode(t *testing.T) {
	// Use a mock that would fail if called
	mock := &mockLLMCaller{
		err: errors.New("LLM should not be called"),
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Mode:   "direct",
		Domain: "general",
	}

	// Empty slice with direct mode should return empty
	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return empty slice for direct mode
	if len(criteria) != 0 {
		t.Fatalf("expected 0 criteria for direct mode, got %d", len(criteria))
	}

	// LLM should NOT have been called
	if len(mock.calls) != 0 {
		t.Error("LLM should not have been called for direct mode fallback")
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
		Mode:   "react",
		Domain: "code",
	}

	rawCriteria := []RawCriterion{
		{ID: "rc_1", Description: "raw criterion", Nature: "objective", Weight: "must"},
	}

	criteria, err := extractor.Enrich(context.Background(), rawCriteria, routing)
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

// TestEnrich_GeneralDomainAlsoGetsMarkdownCriterion tests that general domain
// also gets the Markdown formatting fallback criterion like research.
func TestEnrich_GeneralDomainAlsoGetsMarkdownCriterion(t *testing.T) {
	mock := &mockLLMCaller{
		err: errors.New("LLM should not be called"),
	}

	extractor := NewACExtractor(mock)
	routing := &RoutingDecision{
		Mode:   "plan_execute",
		Domain: "general",
	}

	criteria, err := extractor.Enrich(context.Background(), []RawCriterion{}, routing)
	if err != nil {
		t.Fatalf("Enrich returned error: %v", err)
	}

	// Should return 2 fallback criteria for general domain (includes Markdown criterion)
	if len(criteria) != 2 {
		t.Fatalf("expected 2 fallback criteria for general domain, got %d", len(criteria))
	}
	if criteria[1].ID != "ac_fallback_2" {
		t.Errorf("expected second ID 'ac_fallback_2', got '%s'", criteria[1].ID)
	}
}
