package strutil

import "testing"

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		want     string
	}{
		{name: "empty", input: "", maxBytes: 10, want: ""},
		{name: "shorter than max", input: "hello", maxBytes: 10, want: "hello"},
		{name: "equal to max", input: "hello", maxBytes: 5, want: "hello"},
		{name: "ascii truncation", input: "hello world", maxBytes: 5, want: "hello"},
		{name: "negative max", input: "hello", maxBytes: -1, want: "hello"},
		{name: "zero max", input: "hello", maxBytes: 0, want: "hello"},
		// "café" — "caf" + 0xc3 0xa9 (é). At maxBytes=4 we'd split "é".
		{name: "split multibyte rune is rolled back", input: "café", maxBytes: 4, want: "caf"},
		{name: "exact rune boundary multibyte", input: "café", maxBytes: 5, want: "café"},
		// "你好" — each char is 3 bytes (E4 BD A0, E5 A5 BD).
		{name: "split chinese rune is rolled back to 0", input: "你好", maxBytes: 2, want: ""},
		{name: "split chinese rune at first char", input: "你好", maxBytes: 3, want: "你"},
		{name: "split chinese rune mid second char", input: "你好", maxBytes: 4, want: "你"},
		{name: "all chinese chars", input: "你好", maxBytes: 6, want: "你好"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateUTF8(tt.input, tt.maxBytes)
			if got != tt.want {
				t.Errorf("TruncateUTF8(%q, %d) = %q, want %q", tt.input, tt.maxBytes, got, tt.want)
			}
		})
	}
}

func TestTruncateUTF8AtLineBoundary(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		want     string
	}{
		{name: "empty", input: "", maxBytes: 10, want: ""},
		{name: "short string no truncation", input: "hello", maxBytes: 10, want: "hello"},
		{name: "exact fit", input: "hello", maxBytes: 5, want: "hello"},
		// "你好\nabc" — 你=3 bytes, 好=3 bytes, \n, abc. maxBytes=5 splits 好;
		// TruncateUTF8 rolls back to "你" (rune boundary respected), no newline.
		{name: "multi-byte UTF-8 at boundary", input: "你好\nabc", maxBytes: 5, want: "你"},
		// "line1\nline2\nline3" — maxBytes=13 yields "line1\nline2\nl", snaps to "line1\nline2\n".
		{name: "mid-line truncation snaps to newline", input: "line1\nline2\nline3", maxBytes: 13, want: "line1\nline2\n"},
		{name: "no newline returns as-is", input: "hello world", maxBytes: 5, want: "hello"},
		// "\nhello" — the only newline is at index 0, so idx is not > 0; returned as-is.
		{name: "newline at idx 0 returns as-is", input: "\nhello", maxBytes: 3, want: "\nhe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateUTF8AtLineBoundary(tt.input, tt.maxBytes)
			if got != tt.want {
				t.Errorf("TruncateUTF8AtLineBoundary(%q, %d) = %q, want %q", tt.input, tt.maxBytes, got, tt.want)
			}
		})
	}
}
