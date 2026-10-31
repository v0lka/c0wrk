package session

import "strings"

// invalidToolCallPrefixes classify a tool result as an "invalid tool call"
// (malformation or argument-validation rejection, never runtime/policy
// refusals): specs/contracts/event-catalog.md, agent_metrics.invalid_tool_calls.
var invalidToolCallPrefixes = []string{
	"failed to parse input",
	"validation error:",
	"tool not found:",
	"batch parse error:",
	"batch: no calls provided",
	"error: batch cannot be nested",
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
