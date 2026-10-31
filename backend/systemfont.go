package backend

import "strings"

// pangoStyleKeywords are the Pango style/weight tokens that may sit between the family
// name and the point size ('DejaVu Sans Bold Italic 10'); c0wrk applies weight/slant
// through CSS, so they are stripped, not glued into the family. See specs/domains/frontend/fonts.md.
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

// systemFontPair carries the two raw GVariant font descriptions from
// org.gnome.desktop.interface (UI font, monospace font); both go through
// parseGnomeFontName and are parsed independently. See specs/domains/frontend/fonts.md.
type systemFontPair struct {
	UI   string // org.gnome.desktop.interface font-name
	Mono string // org.gnome.desktop.interface monospace-font-name
}

// parseGnomeFontName extracts the font FAMILY from the raw gsettings value — a Pango
// font description ('Noto Sans 11', 'DejaVu Sans Bold 10', shared by both keys): strips
// the GVariant quoting, the trailing point size and style keywords; ok is false when no
// family survives; the point size is dropped — c0wrk owns its type scale. See specs/domains/frontend/fonts.md.
func parseGnomeFontName(raw string) (family string, ok bool) {
	s := strings.TrimSpace(raw)
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

// isFontSizeToken reports whether tok is a plain decimal ("11", "10.5") — the shape
// of a Pango point size; strconv.ParseFloat is avoided so "1e3"/"+11" never over-strip.
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
