// Package strutil provides shared string helpers for the core layer.
package strutil

import "unicode/utf8"

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
