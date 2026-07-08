package main

import "github.com/v0lka/sp4rk/strutil"

// truncate shortens a string to maxLen bytes, appending "…" if truncated.
// The cut is UTF-8-safe (delegates to strutil.TruncateUTF8) so multi-byte
// runes are never split. Shared by the classic (main.go) and fluent
// (main_fluent.go) example variants for compact tool listings.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return strutil.TruncateUTF8(s, maxLen-1) + "…"
}
