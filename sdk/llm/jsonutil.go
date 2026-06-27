package llm

import (
	"encoding/json"
	"strings"
)

// ExtractJSON extracts JSON from LLM response content, handling markdown
// code blocks (```json ... ``` or ``` ... ```) and finding the longest
// valid JSON object starting from each '{'.
//
// This is the shared utility for parsing structured LLM responses from
// routing, reflection, planning, and other LLM-driven classification
// tasks.
func ExtractJSON(content string) string {
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

	// Fallback: find the last valid JSON object by scanning backwards from the
	// end. This matches the old heuristic (LLMs place their final structured
	// output at the end) and correctly returns the outermost brace pair. The
	// scanner is string-aware to ignore braces inside JSON string values.
	//
	// Backward escape semantics: when scanning right-to-left and we see '"',
	// we must look at the character BEFORE it (to the left, which we haven't
	// yet scanned) to determine if the quote is escaped. We handle this by
	// checking consecutive backslashes to the left.
	for end := len(content) - 1; end >= 0; end-- {
		if content[end] != '}' {
			continue
		}
		// Found a closing brace. Scan backwards to find its matching '{'.
		depth := 0
		inString := false
		// Counts consecutive backslashes seen before encountering a quote.
		// An odd count means the quote is escaped.
	inner:
		for i := end; i >= 0; i-- {
			ch := content[i]

			if inString {
				if ch == '\\' {
					// Count consecutive backslashes to the left to determine
					// if the next quote we encounter is escaped.
					escapeCount := 1
					for j := i - 1; j >= 0 && content[j] == '\\'; j-- {
						escapeCount++
					}
					// If escapeCount is odd, the quote to the left is escaped
					// and we stay in the string; skip over all backslashes.
					if escapeCount%2 == 1 {
						i -= escapeCount - 1
						continue
					}
					// Even count: the quote we passed was NOT escaped.
					// Fall through to normal processing.
				}
				if ch == '"' {
					inString = false
				}
				continue
			}
			if ch == '"' {
				// Entering a string. Check if this quote is escaped.
				escapeCount := 0
				for j := i - 1; j >= 0 && content[j] == '\\'; j-- {
					escapeCount++
				}
				if escapeCount%2 == 1 {
					// Odd backslashes → this quote is escaped, not a string boundary.
					continue
				}
				inString = true
				continue
			}

			switch ch {
			case '}':
				depth++
			case '{':
				depth--
				if depth == 0 {
					candidate := content[i : end+1]
					if json.Valid([]byte(candidate)) {
						return candidate
					}
					// Not valid JSON; this '}' belongs to a different
					// brace pair. Break inner loop to continue scanning
					// backwards for the next '}'.
					break inner
				}
			}
		}
	}

	// Return as-is if nothing found
	return content
}
