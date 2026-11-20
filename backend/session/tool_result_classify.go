package session

import "strings"

// invalidToolCallPrefixes classify a tool result as an "invalid tool call"
// (malformation or argument-validation rejection, never runtime/policy
// refusals): specs/contracts/event-catalog.md, agent_metrics.invalid_tool_calls.
// These cover sp4rk's parse/lookup/batch-shape rejections and the executor's
// batch pre-flight aggregate (`batch rejected: …`, emitted only for schema
// validation). The *tools.InputValidationError render (`input for "TOOL" is
// invalid…`) is matched separately by isInputValidationError below, which
// mirrors sp4rk's own joint discriminator rule.
var invalidToolCallPrefixes = []string{
	"failed to parse input",
	"validation error:",
	"tool not found:",
	"batch parse error:",
	"batch: no calls provided",
	"error: batch cannot be nested",
	"batch rejected:",
}

// isInputValidationError mirrors sp4rk's isValidationError discriminator rule
// (sp4rk agent/executor.go): every *tools.InputValidationError renders as
// `input for "TOOL" is invalid…` in exactly two joint forms (`is invalid: `
// and `is invalid at `), and the batch pre-flight aggregate embeds the same
// renders — covered by `batch rejected:` above. The `input for "TOOL"`
// discriminator alone is NOT enough: any error result could start with it
// (e.g. a future user-rejection render such as
// `input for "x" was rejected by the user`), and free untrusted text carrying
// only the joint forms must not fabricate model-fault metrics either — so
// both sides of the rule are required, as in sp4rk.
func isInputValidationError(c string) bool {
	return strings.HasPrefix(c, `input for "`) &&
		(strings.Contains(c, " is invalid: ") || strings.Contains(c, " is invalid at "))
}

// isInvalidToolCall prefix-matches case- and whitespace-insensitively, so
// classification is stable across message-formatting variations.
func isInvalidToolCall(content string) bool {
	c := strings.ToLower(strings.TrimSpace(content))
	if isInputValidationError(c) {
		return true
	}
	for _, prefix := range invalidToolCallPrefixes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
