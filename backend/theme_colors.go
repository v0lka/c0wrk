package backend

import (
	"strconv"
	"strings"
)

// themePalette defines the allowed icon colors matching the application's One Dark theme.
var themePalette = []string{
	"#252931", // Midnight Navy
	"#282c34", // Dark Charcoal
	"#1d2025", // Obsidian
	"#cccccc", // Silver
	"#528bff", // Vivid Blue
	"#abb2bf", // Pewter
	"#56b6c2", // Teal
	"#61afef", // Sky Blue
	"#c678dd", // Orchid
	"#98c379", // Sage Green
	"#e06c75", // Coral
	"#d19a66", // Amber
	"#be5046", // Rust
	"#e5c07b", // Gold
}

// precomputed RGB values for the theme palette.
var themePaletteRGB []rgb

func init() {
	themePaletteRGB = make([]rgb, len(themePalette))
	for i, hex := range themePalette {
		themePaletteRGB[i] = parseHex(hex)
	}
}

type rgb struct {
	r, g, b int
}

// snapToTheme returns the closest theme palette color to the given hex color.
// If the input is empty or unparseable, it returns the input unchanged.
func snapToTheme(hex string) string {
	if hex == "" {
		return hex
	}

	c := parseHex(hex)
	if c.r < 0 {
		return hex
	}

	bestIdx := 0
	bestDist := colorDistSq(c, themePaletteRGB[0])

	for i := 1; i < len(themePaletteRGB); i++ {
		d := colorDistSq(c, themePaletteRGB[i])
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}

	return themePalette[bestIdx]
}

// colorDistSq returns the squared Euclidean distance between two colors in RGB space.
func colorDistSq(a, b rgb) int {
	dr := a.r - b.r
	dg := a.g - b.g
	db := a.b - b.b
	return dr*dr + dg*dg + db*db
}

// parseHex parses a hex color string (#RGB, #RRGGBB) into RGB components.
// Returns rgb{-1,-1,-1} on failure.
func parseHex(s string) rgb {
	s = strings.TrimPrefix(s, "#")

	switch len(s) {
	case 3:
		// Build strings via a small builder to avoid per-char allocations.
		var b [3]strings.Builder
		b[0].WriteByte(s[0])
		b[0].WriteByte(s[0])
		b[1].WriteByte(s[1])
		b[1].WriteByte(s[1])
		b[2].WriteByte(s[2])
		b[2].WriteByte(s[2])
		r, err := strconv.ParseUint(b[0].String(), 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		g, err := strconv.ParseUint(b[1].String(), 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		bb, err := strconv.ParseUint(b[2].String(), 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		return rgb{int(r), int(g), int(bb)}
	case 6:
		r, err := strconv.ParseUint(s[0:2], 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		g, err := strconv.ParseUint(s[2:4], 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		b, err := strconv.ParseUint(s[4:6], 16, 8)
		if err != nil {
			return rgb{-1, -1, -1}
		}
		return rgb{int(r), int(g), int(b)}
	default:
		return rgb{-1, -1, -1}
	}
}
