package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/internal/core/prompts"
	"github.com/user/agent/internal/llm"
)

// ACExtractor extracts acceptance criteria from user requests.
type ACExtractor struct {
	llm LLMCaller
}

// NewACExtractor creates a new ACExtractor.
func NewACExtractor(caller LLMCaller) *ACExtractor {
	return &ACExtractor{llm: caller}
}

// Extract extracts acceptance criteria from the user message using LLM.
func (e *ACExtractor) Extract(ctx context.Context, userMessage, domain string) ([]AcceptanceCriterion, error) {
	// Build user message with domain context
	userPrompt := fmt.Sprintf("Domain: %s\n\nUser request:\n%s", domain, userMessage)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.ACExtractorSystem},
			{Role: "user", Content: userPrompt},
		},
	}

	resp, err := e.llm.Call(ctx, "router", req)
	if err != nil {
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	content := resp.Message.Content

	// Parse JSON array from response (handle markdown code blocks)
	criteria, err := parseACJSON(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse acceptance criteria: %w", err)
	}

	return criteria, nil
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
