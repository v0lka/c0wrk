package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/prompt"
	"github.com/user/agent/sdk/tools"
)

var (
	codeBlockPattern = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?(\\{.*?\\})\\s*\\n?```")
	jsonPattern      = regexp.MustCompile(`(?s)(\{.*\})`)
)

// Router classifies user requests by complexity and determines execution strategy.
type Router struct {
	llm           LLMCaller
	historyWindow int // number of recent messages to include
	modelRegistry *llm.ModelRegistry
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

// SetModelRegistry sets the model registry for tier resolution.
// If not set, the router defaults to "large" tier.
func (r *Router) SetModelRegistry(registry *llm.ModelRegistry) {
	r.modelRegistry = registry
}

// getTier resolves the model tier, defaulting to "large" if not configured.
func (r *Router) getTier() prompt.ModelTier {
	if r.modelRegistry == nil {
		slog.Debug("router: model tier resolved", "tier", prompt.TierLarge)
		return prompt.TierLarge
	}
	meta, _ := r.modelRegistry.Resolve("")
	tier := prompt.ModelTier(meta.Tier)
	if tier == "" {
		slog.Debug("router: model tier resolved", "tier", prompt.TierLarge)
		return prompt.TierLarge
	}
	slog.Debug("router: model tier resolved", "tier", tier)
	return tier
}

// Route analyzes the user's request and determines the best execution strategy.
func (r *Router) Route(ctx context.Context, userMessage string, availableTools []tools.ToolDescriptor, history []llm.Message) (decision *RoutingDecision, err error) {
	// Build tool list for the prompt (grouped by priority tier)
	toolListStr := agent.BuildGroupedToolList(availableTools)

	// Build system prompt using prompt builder with tier-specific adapters
	systemPrompt := prompt.New(r.getTier()).
		Core(prompts.RouterSystem).
		ForLarge(prompts.RouterLarge).
		ForSmall(prompts.RouterSmall).
		Replace("AVAILABLE-TOOLS", toolListStr).
		Build()

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
		// Retry with repair prompt
		repairMessages := make([]llm.Message, len(messages)+2)
		copy(repairMessages, messages)
		repairMessages[len(messages)] = llm.Message{Role: "assistant", Content: resp.Message.Content}
		repairMessages[len(messages)+1] = llm.Message{
			Role:    "user",
			Content: "Your previous response was not valid JSON. Respond with ONLY a JSON object in this exact format:\n{\"domain\":\"general\",\"complexity\":1,\"needs_clarification\":false}",
		}

		retryResp, retryErr := r.llm.Call(ctx, llm.ChatRequest{Messages: repairMessages})
		if retryErr != nil {
			return nil, fmt.Errorf("failed to parse routing decision: %w", err)
		}

		retryJSON := extractJSON(retryResp.Message.Content)
		if retryErr := json.Unmarshal([]byte(retryJSON), &routingDecision); retryErr != nil {
			return nil, fmt.Errorf("failed to parse routing decision after retry: %w", retryErr)
		}
	}

	validateRoutingDecision(&routingDecision)

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

// validateRoutingDecision sanitizes and corrects a routing decision from LLM output.
func validateRoutingDecision(d *RoutingDecision) {
	// Validate domain
	switch d.Domain {
	case "code", "research", "general", "mixed":
		// valid
	default:
		d.Domain = "general"
	}

	// Clamp complexity to [1, 5]
	if d.Complexity < 1 {
		d.Complexity = 1
	}
	if d.Complexity > 5 {
		d.Complexity = 5
	}
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
