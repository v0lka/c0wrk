package backend

import "testing"

func TestSnapToTheme(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Exact matches should stay the same.
		{"#528bff", "#528bff"},
		{"#98c379", "#98c379"},
		{"#e06c75", "#e06c75"},
		// nvim-web-devicons green (#89E051) should snap to Sage Green (#98c379).
		{"#89E051", "#98c379"},
		// nvim-web-devicons orange (#EA7600) is closest to Rust (#be5046) in RGB space.
		{"#EA7600", "#be5046"},
		// nvim-web-devicons purple (#A074C4) should snap to Orchid (#c678dd).
		{"#A074C4", "#c678dd"},
		// Empty and unparseable inputs pass through.
		{"", ""},
		{"invalid", "invalid"},
		// Short hex (#RGB) form.
		{"#fff", "#cccccc"},
	}

	for _, tt := range tests {
		got := snapToTheme(tt.input)
		if got != tt.want {
			t.Errorf("snapToTheme(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseHex(t *testing.T) {
	tests := []struct {
		input string
		want  rgb
	}{
		{"#528bff", rgb{0x52, 0x8b, 0xff}},
		{"528bff", rgb{0x52, 0x8b, 0xff}},
		{"#abc", rgb{0xaa, 0xbb, 0xcc}},
		{"", rgb{-1, -1, -1}},
		{"#zzzzzz", rgb{-1, -1, -1}},
	}

	for _, tt := range tests {
		got := parseHex(tt.input)
		if got != tt.want {
			t.Errorf("parseHex(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
