// Package strutil provides string utilities such as UTF-8-safe truncation.
package strutil

import (
	"strings"
	"unicode/utf8"
)

// TruncateUTF8 returns s truncated to at most maxBytes bytes, respecting
// UTF-8 rune boundaries so the result is always valid UTF-8. If s is already
// shorter than maxBytes (or maxBytes is non-positive), s is returned unchanged.
//
// This is the recommended replacement for byte-slice truncation expressions
// like s[:N] when the input may contain multi-byte UTF-8 characters that the
// downstream consumer (LLM API, logger, frontend) expects to be valid.
func TruncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// TruncateUTF8AtLineBoundary truncates s to at most maxBytes bytes using
// TruncateUTF8, then snaps the result back to the last newline so the
// returned string ends on a complete line. If the truncated string contains
// no newline, or the only newline is at index 0, the UTF-8-safe truncated
// value is returned unchanged.
//
// WARNING: snapping to the last newline can shorten the result significantly
// below maxBytes — in the extreme, input with one early newline followed by a
// single very long line is cut down to just that first line. Callers that
// need to preserve as much content as possible should use TruncateUTF8
// instead and tolerate a partial final line.
func TruncateUTF8AtLineBoundary(s string, maxBytes int) string {
	truncated := TruncateUTF8(s, maxBytes)
	if idx := strings.LastIndex(truncated, "\n"); idx > 0 {
		return truncated[:idx+1]
	}
	return truncated
}
