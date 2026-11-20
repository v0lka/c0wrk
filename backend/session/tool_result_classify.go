package session

import "strings"

// invalidToolCallPrefixes classify a tool result as an "invalid tool call"
// (malformation or argument-validation rejection, never runtime/policy
// refusals): specs/contracts/event-catalog.md, agent_metrics.invalid_tool_calls.
// The last two cover sp4rk's *tools.InputValidationError render
// (`input for "TOOL" is invalid…`, top-level and nested `at <path>` forms)
// and the executor's batch pre-flight aggregate (`batch rejected: …`).
var invalidToolCallPrefixes = []string{
	"failed to parse input",
	"validation error:",
	"tool not found:",
	"batch parse error:",
	"batch: no calls provided",
	"error: batch cannot be nested",
	`input for "`,
	"batch rejected:",
}

// isInvalidToolCall prefix-matches case- and whitespace-insensitively, so
// classification is stable across message-formatting variations.
func isInvalidToolCall(content string) bool {
	c := strings.ToLower(strings.TrimSpace(content))
	for _, prefix := range invalidToolCallPrefixes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
