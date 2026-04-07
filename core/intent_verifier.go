package core

import (
	"context"
	"log/slog"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// readOnlyToolWhitelist defines the tools that the intent verifier is allowed to use.
// These are read-only tools that cannot modify the workspace.
var readOnlyToolWhitelist = map[string]bool{
	"read_file": true,
	"list_dir":  true,
	"grep":      true,
	"find_file": true,
}

// IntentVerifier is a mini-agent that performs Tier 2 intent-based verification.
// It compares the user's original request against the delivered output and workspace
// changes, using read-only file tools to inspect the actual workspace state.
type IntentVerifier struct {
	llm              LLMCaller
	toolRegistry     *tools.ToolRegistry
	tokenCounter     llm.TokenCounter
	contextFactory   ContextManagerFactory
	maxSteps         int
	logger           *slog.Logger
	emitter          Emitter
	toolResultBudget ToolResultBudget
}

// NewIntentVerifier creates a new IntentVerifier.
func NewIntentVerifier(
	llmCaller LLMCaller,
	toolRegistry *tools.ToolRegistry,
	tokenCounter llm.TokenCounter,
	contextFactory ContextManagerFactory,
	logger *slog.Logger,
	emitter Emitter,
	toolResultBudget ToolResultBudget,
) *IntentVerifier {
	return &IntentVerifier{
		llm:              llmCaller,
		toolRegistry:     toolRegistry,
		tokenCounter:     tokenCounter,
		contextFactory:   contextFactory,
		maxSteps:         15,
		logger:           logger,
		emitter:          emitter,
		toolResultBudget: toolResultBudget,
	}
}

// intentVerifierTaskTemplate is the task description template for the verifier.
const intentVerifierTaskTemplate = `## Original User Request
USER_MESSAGE

## Agent's Final Output
FINAL_OUTPUT

## Workspace Changes Summary
CHANGE_SUMMARY`

// Verify runs intent verification and returns the result.
func (v *IntentVerifier) Verify(ctx context.Context, userMessage, finalOutput, changeSummary string) (*IntentVerification, error) {
	// 1. Build system prompt
	systemPrompt := prompts.IntentVerifierSystem

	// 2. Build task description from template
	taskDescription := intentVerifierTaskTemplate
	taskDescription = strings.ReplaceAll(taskDescription, "USER_MESSAGE", userMessage)
	taskDescription = strings.ReplaceAll(taskDescription, "FINAL_OUTPUT", finalOutput)
	taskDescription = strings.ReplaceAll(taskDescription, "CHANGE_SUMMARY", changeSummary)

	// 3. Filter tools to read-only subset
	filteredTools := filterReadOnlyTools(v.toolRegistry.List())

	// 4. Model metadata — use reasonable defaults for the verifier
	modelMeta := llm.ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}

	// 5. Create context manager
	cm := v.contextFactory(systemPrompt, modelMeta, "sliding_window")

	// 6. Set task (no acceptance criteria for intent verifier)
	cm.SetTask(taskDescription, nil)

	// 7. Create executor — suppress assistant events, use noop emitter
	exec := NewExecutor(
		v.llm,
		v.toolRegistry,
		v.tokenCounter,
		v.maxSteps,
		v.logger,
		(*agent.NoopEvents)(nil),
		true, // suppressAssistantEvents
		v.toolResultBudget,
	)

	// 8. Run the verification agent
	result, err := exec.Run(ctx, filteredTools, cm)
	if err != nil {
		return nil, err
	}

	// 9. Parse the output
	passed, feedback := parseIntentVerdict(result.Output)

	return &IntentVerification{
		Passed:   passed,
		Feedback: feedback,
		Steps:    result.Steps,
	}, nil
}

// filterReadOnlyTools returns only tool descriptors whose Name is in the read-only whitelist.
func filterReadOnlyTools(allTools []tools.ToolDescriptor) []tools.ToolDescriptor {
	filtered := make([]tools.ToolDescriptor, 0, len(readOnlyToolWhitelist))
	for _, td := range allTools {
		if readOnlyToolWhitelist[td.Name] {
			filtered = append(filtered, td)
		}
	}
	return filtered
}

// parseIntentVerdict parses the verifier's output into a pass/fail verdict and feedback.
// The output is expected to start with "YES" or "NO" on the first line.
func parseIntentVerdict(output string) (passed bool, feedback string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return false, ""
	}

	// Split into first line and rest
	firstLine := output
	rest := ""
	if idx := strings.IndexByte(output, '\n'); idx >= 0 {
		firstLine = output[:idx]
		rest = strings.TrimSpace(output[idx+1:])
	}

	upper := strings.ToUpper(strings.TrimSpace(firstLine))
	passed = strings.HasPrefix(upper, "YES")

	// Feedback is everything after the YES/NO line
	if rest != "" {
		feedback = rest
	} else {
		// If no newline, feedback is the part after YES/NO
		if strings.HasPrefix(upper, "YES") && len(firstLine) > 3 {
			feedback = strings.TrimSpace(firstLine[3:])
		} else if strings.HasPrefix(upper, "NO") && len(firstLine) > 2 {
			feedback = strings.TrimSpace(firstLine[2:])
		}
	}

	return passed, feedback
}
