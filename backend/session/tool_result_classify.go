package session

import "strings"

// invalidToolCallPrefixes are the error-message prefixes that classify a tool
// result as an "invalid tool call": the model produced a call the executor
// could not map to a valid invocation — either structurally (malformed input,
// unknown tool, or a structurally invalid batch) or semantically (a tool's
// argument validation rejected the call, e.g. a "validation error:" from the
// goal/plan/reflect tools). Runtime failures (shell errors, missing files) and
// security/policy/HITL refusals deliberately do NOT appear here.
var invalidToolCallPrefixes = []string{
	"failed to parse input",
	"validation error:",
	"tool not found:",
	"batch parse error:",
	"batch: no calls provided",
	"error: batch cannot be nested",
}

// isInvalidToolCall reports whether a tool-result error message is one of the
// recognized "invalid tool call" forms (structural malformations and semantic
// argument-validation rejections alike). Matching is case-insensitive and
// ignores leading/trailing whitespace, so classifier behavior is stable across
// message-formatting variations.
func isInvalidToolCall(content string) bool {
	c := strings.ToLower(strings.TrimSpace(content))
	for _, prefix := range invalidToolCallPrefixes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
