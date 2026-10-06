package backend

import "strings"

// pangoStyleKeywords are the Pango font-description style/weight tokens that
// may sit between the family name and the point size in a gsettings font
// value (e.g. 'DejaVu Sans Bold Italic 10'). They describe the style, not the
// family: c0wrk applies weight/slant through CSS, so a trailing one is
// stripped instead of being glued into the family name (a "DejaVu Sans Bold"
// family would silently fall back to the default face, or worse, resolve to a
// different font than configured).
var pangoStyleKeywords = map[string]struct{}{
	"Bold":    {},
	"Italic":  {},
	"Oblique": {},
	"Light":   {},
	"Regular": {},
	"Medium":  {},
	"Heavy":   {},
	"Thin":    {},
}

// systemFontPair carries the two raw GVariant font descriptions read from
// the org.gnome.desktop.interface schema: the interface (UI) font and the
// monospace font. Both are Pango font descriptions ('Noto Sans 11',
// 'DejaVu Sans Mono Bold 10') and both go through parseGnomeFontName; the
// two are parsed independently, so an unparsable value empties only its own
// family.
type systemFontPair struct {
	UI   string // org.gnome.desktop.interface font-name
	Mono string // org.gnome.desktop.interface monospace-font-name
}

// parseGnomeFontName extracts the font FAMILY from the raw value printed by
// `gsettings get org.gnome.desktop.interface <key>` — a Pango font
// description such as `'Noto Sans 11'`, `'DejaVu Sans Bold 10'` or
// `'Cantarell 10.5'`. It is used for both the UI font (font-name) and the
// monospace font (monospace-font-name), which share the same serialization.
// It strips the GVariant quoting (single or double), the trailing point size
// (integer or decimal) and any trailing style keywords (see
// pangoStyleKeywords), returning what remains as the family name.
//
// ok is false when no family survives — an empty value, a lone size token
// ("'11'") or whitespace-only garbage. The point size is deliberately NOT
// returned: c0wrk owns its type scale (14px base + the UI Scale setting), and
// GNOME's point size would fight both.
func parseGnomeFontName(raw string) (family string, ok bool) {
	s := strings.TrimSpace(raw)
	// gsettings prints GVariant strings with surrounding quotes; both single
	// and double quoting are legal serialization choices.
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	if s == "" {
		return "", false
	}
	fields := strings.Fields(s)
	for len(fields) > 0 {
		last := fields[len(fields)-1]
		if isFontSizeToken(last) {
			fields = fields[:len(fields)-1]
			continue
		}
		// A style keyword is stripped only while a family token remains
		// below it: a lone "Bold" value keeps "Bold" as the family rather
		// than collapsing to an empty name.
		if _, isKeyword := pangoStyleKeywords[last]; isKeyword && len(fields) > 1 {
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return "", false
	}
	return strings.Join(fields, " "), true
}

// isFontSizeToken reports whether tok is a plain decimal number (digits with
// at most one dot, e.g. "11" or "10.5") — the shape of a Pango point size.
// strconv.ParseFloat is deliberately avoided: it accepts forms like "1e3" or
// "+11" that never appear as font sizes but would over-strip a family token.
func isFontSizeToken(tok string) bool {
	if tok == "" {
		return false
	}
	dots := 0
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
		case r == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
