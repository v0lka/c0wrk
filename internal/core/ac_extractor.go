package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/internal/core/prompts"
	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// ACExtractor extracts acceptance criteria from user requests.
type ACExtractor struct {
	llm LLMCaller
}

// NewACExtractor creates a new ACExtractor.
func NewACExtractor(caller LLMCaller) *ACExtractor {
	return &ACExtractor{llm: caller}
}

// ExtractRaw extracts domain-agnostic raw criteria from the user message (Phase 1).
// This runs BEFORE the Router to provide structured context for routing decisions.
func (e *ACExtractor) ExtractRaw(ctx context.Context, userMessage string) ([]RawCriterion, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.RawACExtractorSystem},
			{Role: "user", Content: userMessage},
		},
	}

	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("raw AC extraction LLM call failed: %w", err)
	}

	criteria, err := parseRawACJSON(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw acceptance criteria: %w", err)
	}

	return criteria, nil
}

// parseRawACJSON extracts and parses JSON from content into RawCriterion slice.
func parseRawACJSON(content string) ([]RawCriterion, error) {
	content = strings.TrimSpace(content)

	// Handle markdown code blocks
	if strings.HasPrefix(content, "```") {
		start := strings.Index(content, "[")
		if start == -1 {
			start = strings.Index(content, "\n") + 1
		}
		end := strings.LastIndex(content, "```")
		if end > start {
			content = strings.TrimSpace(content[start:end])
		}
	}

	// Handle empty response
	if content == "" || content == "[]" {
		return []RawCriterion{}, nil
	}

	var criteria []RawCriterion
	if err := json.Unmarshal([]byte(content), &criteria); err != nil {
		return nil, err
	}

	return criteria, nil
}

// Enrich transforms raw criteria into final AcceptanceCriteria using domain-specific logic (Phase 2).
// This runs AFTER the Router, using the routing decision for domain-aware enrichment.
func (e *ACExtractor) Enrich(ctx context.Context, rawCriteria []RawCriterion, routing *RoutingDecision) ([]AcceptanceCriterion, error) {
	// Fallback: when raw criteria are empty/nil, generate minimal criteria from routing context
	if len(rawCriteria) == 0 {
		return e.fallbackCriteria(routing), nil
	}

	// Build context from routing decision and raw criteria
	rawJSON, err := json.Marshal(rawCriteria)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal raw criteria: %w", err)
	}

	contextLines := []string{
		"Domain: " + routing.Domain,
		"Mode: " + routing.Mode,
		fmt.Sprintf("Complexity: %d", routing.Complexity),
		"Suggested tools: " + strings.Join(routing.SuggestedTools, ", "),
	}
	if ws := tools.WorkspacePathFrom(ctx); ws != "" {
		contextLines = append(contextLines, "Workspace: "+ws)
	}
	userPrompt := fmt.Sprintf("%s\n\nRaw criteria:\n%s", strings.Join(contextLines, "\n"), string(rawJSON))

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.ACEnricherSystem},
			{Role: "user", Content: userPrompt},
		},
	}

	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("AC enrichment LLM call failed: %w", err)
	}

	criteria, err := parseACJSON(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse enriched acceptance criteria: %w", err)
	}

	return criteria, nil
}

// fallbackCriteria generates minimal acceptance criteria when raw extraction produced no results.
func (e *ACExtractor) fallbackCriteria(routing *RoutingDecision) []AcceptanceCriterion {
	// Direct mode: no criteria needed (trivial task or will escalate if eval fails)
	if routing.Mode == "direct" {
		return []AcceptanceCriterion{}
	}
	// React/plan_execute: generate minimal evaluable criteria
	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_fallback_1",
			Description: "Task objective has been addressed using available tools",
			CheckType:   "llm_judge",
		},
	}
	if routing.Domain == "research" || routing.Domain == "general" {
		criteria = append(criteria, AcceptanceCriterion{
			ID:          "ac_fallback_2",
			Description: "Response uses proper Markdown formatting with headers and structure",
			CheckType:   "llm_judge",
		})
	}
	return criteria
}

// parseACJSON extracts and parses JSON from content, handling code blocks.
func parseACJSON(content string) ([]AcceptanceCriterion, error) {
	content = strings.TrimSpace(content)

	// Handle markdown code blocks
	if strings.HasPrefix(content, "```") {
		// Find start of JSON
		start := strings.Index(content, "[")
		if start == -1 {
			start = strings.Index(content, "\n") + 1
		}
		// Find end of code block
		end := strings.LastIndex(content, "```")
		if end > start {
			content = strings.TrimSpace(content[start:end])
		}
	}

	// Handle empty response
	if content == "" || content == "[]" {
		return []AcceptanceCriterion{}, nil
	}

	var criteria []AcceptanceCriterion
	if err := json.Unmarshal([]byte(content), &criteria); err != nil {
		return nil, err
	}

	return criteria, nil
}
