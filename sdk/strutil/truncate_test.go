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
