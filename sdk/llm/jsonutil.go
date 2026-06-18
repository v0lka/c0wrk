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

	// Fallback: find the last '}' and its matching '{' — a single
	// pass rather than O(n²) scanning every brace pair.
	if lastBrace := strings.LastIndex(content, "}"); lastBrace >= 0 {
		if openBrace := strings.LastIndex(content[:lastBrace], "{"); openBrace >= 0 {
			candidate := content[openBrace : lastBrace+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}

	// Return as-is if nothing found
	return content
}
