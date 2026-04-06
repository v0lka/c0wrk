package core

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

var (
	codeBlockPattern = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?(\\{.*?\\})\\s*\\n?```")
	jsonPattern      = regexp.MustCompile(`(?s)(\{.*\})`)
)

// Router classifies user requests by complexity and determines execution strategy.
type Router struct {
	llm           LLMCaller // reuse the interface from executor.go
	historyWindow int       // number of recent messages to include
}

// NewRouter creates a new Router.
func NewRouter(caller LLMCaller, historyWindow int) *Router {
	if historyWindow <= 0 {
		historyWindow = 10
	}
	return &Router{
		llm:           caller,
		historyWindow: historyWindow,
	}
}

// Route analyzes the user's request and determines the best execution strategy.
func (r *Router) Route(ctx context.Context, userMessage string, rawCriteria []RawCriterion, availableTools []tools.ToolDescriptor, history []llm.Message) (decision *RoutingDecision, err error) {
	// Build tool list for the prompt (grouped by priority tier)
	toolListStr := buildGroupedToolList(availableTools)

	// Build raw criteria summary for the prompt
	var criteriaList string
	switch {
	case len(rawCriteria) > 0:
		var cb strings.Builder
		for _, rc := range rawCriteria {
			implicit := ""
			if rc.Implicit {
				implicit = " [implicit]"
			}
			fmt.Fprintf(&cb, "- %s: %s (nature: %s, weight: %s%s)\n", rc.ID, rc.Description, rc.Nature, rc.Weight, implicit)
		}
		criteriaList = cb.String()
	case rawCriteria == nil:
		criteriaList = "(extraction failed — complexity unknown, rely on tool-availability heuristic)"
	default:
		criteriaList = "(none — task appears trivial)"
	}

	// Build system prompt
	systemPrompt := strings.ReplaceAll(prompts.RouterSystem, "AVAILABLE-TOOLS", toolListStr)
	systemPrompt = strings.ReplaceAll(systemPrompt, "RAW-CRITERIA", criteriaList)

	// Build messages for the request
	messages := make([]llm.Message, 0, len(history)+2)
	messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})

	// Add recent history messages (up to historyWindow)
	historyStart := 0
	if len(history) > r.historyWindow {
		historyStart = len(history) - r.historyWindow
	}
	messages = append(messages, history[historyStart:]...)

	// Add user message with the request to classify
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: "Classify this request: " + userMessage,
	})

	// Create chat request
	req := llm.ChatRequest{
		Messages: messages,
	}

	// Call LLM
	resp, err := r.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("router LLM call failed: %w", err)
	}

	// Extract JSON from response (handle markdown code blocks)
	jsonStr := extractJSON(resp.Message.Content)

	// Unmarshal into RoutingDecision
	var routingDecision RoutingDecision
	if err := json.Unmarshal([]byte(jsonStr), &routingDecision); err != nil {
		return nil, fmt.Errorf("failed to parse routing decision: %w", err)
	}

	// Validate and apply defaults
	if routingDecision.CompactionStrategy == "" {
		routingDecision.CompactionStrategy = applyCompactionStrategy(routingDecision.Domain, routingDecision.Complexity)
	}

	return &routingDecision, nil
}

// extractJSON extracts JSON from the response content, handling markdown code blocks.
func extractJSON(content string) string {
	content = strings.TrimSpace(content)

	// Try to extract from markdown code block
	// Pattern: ```json\n{...}\n``` or ```\n{...}\n```
	matches := codeBlockPattern.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// If no code block, try to find raw JSON object
	matches = jsonPattern.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// Return as-is if nothing found
	return content
}

// applyCompactionStrategy applies the domain-based compaction strategy rule.
func applyCompactionStrategy(domain string, complexity int) string {
	switch domain {
	case "code":
		return "sliding_window"
	case "research":
		return "summarization"
	case "mixed", "general":
		if complexity >= 4 {
			return "hierarchical"
		}
		return "sliding_window"
	default:
		return "sliding_window"
	}
}
