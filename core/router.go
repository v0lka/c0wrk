package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/core/skills"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/prompt"
	"github.com/user/agent/sdk/tools"
)

// Router classifies user requests by complexity and determines execution strategy.
type Router struct {
	llm                 LLMCaller
	historyWindow       int // number of recent messages to include
	modelRegistry       *llm.ModelRegistry
	baseReasoningEffort llm.ReasoningEffort
	roleOverrides       map[string]string
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

// SetModelRegistry sets the model registry for model metadata resolution.
func (r *Router) SetModelRegistry(registry *llm.ModelRegistry) {
	r.modelRegistry = registry
}

// SetBaseReasoningEffort sets the base reasoning effort for the router.
func (r *Router) SetBaseReasoningEffort(effort llm.ReasoningEffort) {
	r.baseReasoningEffort = effort
}

// SetRoleOverrides sets the per-role reasoning effort overrides.
func (r *Router) SetRoleOverrides(overrides map[string]string) {
	r.roleOverrides = overrides
}

// Route analyzes the user's request and determines the best execution strategy.
// availableSkills are included in the routing prompt so the LLM can match relevant skills.
func (r *Router) Route(ctx context.Context, userMessage string, availableTools []tools.ToolDescriptor, history []llm.Message, availableSkills []skills.SkillDescriptor) (decision *RoutingDecision, err error) {
	// Build tool list for the prompt (grouped by priority tier)
	toolListStr := agent.BuildGroupedToolList(availableTools)

	// Build skill list for the prompt
	skillListStr := formatSkillList(availableSkills)

	// Build system prompt
	systemPrompt := prompt.NewBuilder().
		Core(prompts.RouterSystem).
		Replace("AVAILABLE-TOOLS", toolListStr).
		Replace("AVAILABLE-SKILLS", skillListStr).
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
	reasoningEffort := llm.ResolveAgentReasoningMode("router", r.baseReasoningEffort, r.roleOverrides)
	req := llm.ChatRequest{
		Messages:        messages,
		ReasoningEffort: reasoningEffort,
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
		repairMessages[len(messages)] = llm.Message{Role: "assistant", Content: resp.Message.Content, ReasoningContent: resp.Message.ReasoningContent}
		repairMessages[len(messages)+1] = llm.Message{
			Role:    "user",
			Content: "Your previous response was not valid JSON. Respond with ONLY a JSON object in this exact format:\n{\"domain\":\"general\",\"complexity\":1,\"needs_clarification\":false}",
		}

		retryResp, retryErr := r.llm.Call(ctx, llm.ChatRequest{Messages: repairMessages, ReasoningEffort: reasoningEffort})
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
// Uses json.Valid to find the longest valid JSON object starting from each '{'.
func extractJSON(content string) string {
	content = strings.TrimSpace(content)

	// Try to extract from markdown code block first.
	// Look for ```json ... ``` or ``` ... ``` blocks.
	if idx := strings.Index(content, "```"); idx >= 0 {
		after := content[idx+3:]
		// Skip optional language tag (e.g., "json")
		if nl := strings.IndexByte(after, '\n'); nl >= 0 {
			after = after[nl+1:]
		}
		if end := strings.Index(after, "```"); end >= 0 {
			block := strings.TrimSpace(after[:end])
			if json.Valid([]byte(block)) {
				return block
			}
		}
	}

	// Find the longest valid JSON object by scanning for '{' and testing
	// progressively larger substrings until json.Valid succeeds.
	for i := 0; i < len(content); i++ {
		if content[i] != '{' {
			continue
		}
		// Scan from the end backwards to find the longest valid JSON.
		for j := len(content); j > i; j-- {
			if content[j-1] != '}' {
				continue
			}
			candidate := content[i:j]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
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

	// Deduplicate and trim matched_skills
	if len(d.MatchedSkills) > 0 {
		seen := make(map[string]bool, len(d.MatchedSkills))
		clean := d.MatchedSkills[:0]
		for _, s := range d.MatchedSkills {
			if s != "" && !seen[s] {
				seen[s] = true
				clean = append(clean, s)
			}
		}
		d.MatchedSkills = clean
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

// formatSkillList formats available skill descriptors for the router prompt.
// Returns "None" if no skills are available.
func formatSkillList(availableSkills []skills.SkillDescriptor) string {
	if len(availableSkills) == 0 {
		return "None"
	}
	var sb strings.Builder
	for i, s := range availableSkills {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("- " + s.Name + ": " + s.Description)
	}
	return sb.String()
}
