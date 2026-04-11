package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
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
func (ac *ACExtractor) ExtractRaw(ctx context.Context, userMessage string) ([]RawCriterion, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.RawACExtractorSystem},
			{Role: "user", Content: userMessage},
		},
	}

	resp, err := ac.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("raw AC extraction LLM call failed: %w", err)
	}

	criteria, err := parseRawACJSON(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw acceptance criteria: %w", err)
	}

	return criteria, nil
}

// extractJSONContent strips markdown code fences and whitespace from LLM output.
func extractJSONContent(content string) string {
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
	return content
}

// parseRawACJSON extracts and parses JSON from content into RawCriterion slice.
func parseRawACJSON(content string) ([]RawCriterion, error) {
	content = extractJSONContent(content)
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
func (ac *ACExtractor) Enrich(ctx context.Context, rawCriteria []RawCriterion, routing *RoutingDecision, userMessage string) ([]AcceptanceCriterion, error) {
	// Fallback: when raw criteria are empty/nil, ask the LLM to formulate a single criterion
	if len(rawCriteria) == 0 {
		return ac.fallbackCriteria(ctx, userMessage, routing)
	}

	// Build context from routing decision and raw criteria
	rawJSON, err := json.Marshal(rawCriteria)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal raw criteria: %w", err)
	}

	contextLines := []string{
		"Domain: " + routing.Domain,
		fmt.Sprintf("Complexity: %d", routing.Complexity),
		"Suggested tools: " + strings.Join(routing.SuggestedTools, ", "),
	}
	if ws := tools.WorkspacePathFrom(ctx); ws != "" {
		contextLines = append(contextLines, "Workspace: "+ws)
		if meta := detectProjectMeta(ws); meta != "" {
			contextLines = append(contextLines, "Project metadata: "+meta)
		}
	}
	userPrompt := fmt.Sprintf("%s\n\nRaw criteria:\n%s", strings.Join(contextLines, "\n"), string(rawJSON))

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.ACEnricherSystem},
			{Role: "user", Content: userPrompt},
		},
	}

	resp, err := ac.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("AC enrichment LLM call failed: %w", err)
	}

	criteria, err := parseACJSON(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse enriched acceptance criteria: %w", err)
	}

	return criteria, nil
}

// fallbackCriteria calls the LLM to formulate a single acceptance criterion
// when raw extraction produced no results.
func (ac *ACExtractor) fallbackCriteria(ctx context.Context, userMessage string, routing *RoutingDecision) ([]AcceptanceCriterion, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.ACFallbackSystem},
			{Role: "user", Content: userMessage},
		},
	}

	resp, err := ac.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fallback AC LLM call failed: %w", err)
	}

	criterion, err := parseFallbackJSON(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fallback acceptance criterion: %w", err)
	}

	criterion.CheckType = "llm_judge"
	return []AcceptanceCriterion{criterion}, nil
}

// parseFallbackJSON extracts and parses a single JSON object from the fallback LLM response.
func parseFallbackJSON(content string) (AcceptanceCriterion, error) {
	content = extractJSONObjectContent(content)
	if content == "" {
		return AcceptanceCriterion{}, errors.New("empty fallback response")
	}
	var criterion AcceptanceCriterion
	if err := json.Unmarshal([]byte(content), &criterion); err != nil {
		return AcceptanceCriterion{}, err
	}
	return criterion, nil
}

// extractJSONObjectContent strips markdown code fences from LLM output containing a JSON object.
// It handles fenced blocks even when they don't appear at position 0 (e.g. preceded by narration),
// and falls back to extracting from first '{' to last '}' when no fences are present.
func extractJSONObjectContent(content string) string {
	content = strings.TrimSpace(content)

	// Strip fenced block if present anywhere, not only at prefix.
	if idx := strings.Index(content, "```"); idx != -1 {
		end := strings.LastIndex(content, "```")
		if end > idx {
			content = strings.TrimSpace(content[idx+3 : end])
		}
	}

	// If there is a JSON object inside, slice from first '{' to last '}'.
	if start := strings.Index(content, "{"); start != -1 {
		if end := strings.LastIndex(content, "}"); end != -1 && end > start {
			content = content[start : end+1]
		}
	}

	return strings.TrimSpace(content)
}

// parseACJSON extracts and parses JSON from content, handling code blocks.
func parseACJSON(content string) ([]AcceptanceCriterion, error) {
	content = extractJSONContent(content)
	if content == "" || content == "[]" {
		return []AcceptanceCriterion{}, nil
	}
	var criteria []AcceptanceCriterion
	if err := json.Unmarshal([]byte(content), &criteria); err != nil {
		return nil, err
	}
	return criteria, nil
}

// projectIndicator maps build/config file names to project metadata strings.
var projectIndicators = []struct {
	file string
	meta string
}{
	{"go.mod", "Go project (build: go build ./..., test: go test ./..., lint: golangci-lint run)"},
	{"Cargo.toml", "Rust project (build: cargo build, test: cargo test, lint: cargo clippy)"},
	{"package.json", "Node.js/TypeScript project (build: npm run build, test: npm test, lint: npm run lint)"},
	{"pyproject.toml", "Python project (test: pytest, lint: ruff check)"},
	{"requirements.txt", "Python project (test: pytest)"},
	{"pom.xml", "Java/Maven project (build: mvn compile, test: mvn test)"},
	{"build.gradle", "Java/Gradle project (build: gradle build, test: gradle test)"},
	{"Makefile", "Makefile detected (run: make)"},
}

// detectProjectMeta checks the workspace root for common build files
// and returns a brief metadata string to help the enricher select correct commands.
// Returns empty string if nothing is detected.
func detectProjectMeta(workspacePath string) string {
	if workspacePath == "" {
		return ""
	}
	var found []string
	for _, ind := range projectIndicators {
		if _, err := os.Stat(filepath.Join(workspacePath, ind.file)); err == nil {
			found = append(found, ind.meta)
		}
	}
	return strings.Join(found, "; ")
}
