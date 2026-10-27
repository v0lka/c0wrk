package backend

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// User theme type constants as exposed in ThemeDTO.Type.
const (
	themeTypeDark  = "dark"
	themeTypeLight = "light"
)

// maxThemeCSSSize is the maximum accepted theme CSS size: 512 KiB — anything
// larger is malformed or carrying assets. See specs/domains/theming.md.
const maxThemeCSSSize = 512 * 1024

// reservedThemeIDs are the built-in themes' own slugs plus the `dark`/`light`
// data-theme attribute keys, so an import can never alias a built-in block.
// See specs/domains/theming.md.
var reservedThemeIDs = map[string]struct{}{
	"default-dark":  {},
	"default-light": {},
	"dark":          {},
	"light":         {},
}

// ThemeDTO is a user-theme descriptor for the frontend: ID is the stable slug
// (the installed CSS file's name stem), Type is "dark" or "light", and CSS
// carries the theme body so activation needs no second RPC.
// See specs/domains/theming.md.
type ThemeDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	CSS  string `json:"css"`
}

// themeHeaderRe matches the metadata header that must OPEN a theme file:
// `/* c0wrk-theme: <name> | <dark|light> */`, anchored to the file start — a
// marker elsewhere in the body is not a header. See specs/domains/theming.md.
var themeHeaderRe = regexp.MustCompile(`(?is)^\s*/\*\s*c0wrk-theme\s*:\s*(.+?)\s*\|\s*(dark|light)\s*\*/`)

// colorSchemeRe matches a `color-scheme: dark|light` body declaration; the
// preceding character may not be a word character or hyphen, so the
// `prefers-color-scheme` media feature cannot pose as a type declaration.
// See specs/domains/theming.md.
var colorSchemeRe = regexp.MustCompile(`(?i)(?:^|[^-\w])color-scheme\s*:\s*["']?(dark|light)\b`)

// Slug normalization helpers: disallowed character runs and repeated hyphens
// both collapse to a single hyphen.
var (
	slugDisallowedRe = regexp.MustCompile(`[^a-z0-9-]+`)
	slugDashesRe     = regexp.MustCompile(`-{2,}`)
)

// ParseThemeCSS extracts a theme's display name and type via the spec's
// fallback chain (metadata header → prettified file name → `color-scheme:`
// declaration → "dark"); it never fails. See specs/domains/theming.md.
func ParseThemeCSS(filename, content string) (name, typ string) {
	name, typ = "", ""
	if m := themeHeaderRe.FindStringSubmatch(content); m != nil {
		name = strings.TrimSpace(m[1])
		typ = strings.ToLower(m[2])
	}
	if name == "" {
		name = prettyThemeName(filename)
	}
	if typ == "" {
		if m := colorSchemeRe.FindStringSubmatch(content); m != nil {
			typ = strings.ToLower(m[1])
		} else {
			typ = themeTypeDark
		}
	}
	return name, typ
}

// ValidateThemeCSS gates a CSS document for install as a user theme (the
// webview injects it, so it is untrusted input): structural sanitization via
// sanitizeThemeCSS, the --color-background/--color-foreground requirement,
// and the maxThemeCSSSize cap. See specs/domains/theming.md.
func ValidateThemeCSS(content string) error {
	if len(content) > maxThemeCSSSize {
		return fmt.Errorf("theme CSS is too large: %d bytes (limit %d)", len(content), maxThemeCSSSize)
	}
	_, err := sanitizeThemeCSS(content)
	return err
}

// themeSlug derives the stable theme id from a CSS file name (the spec's slug
// rules); an empty or reserved id is an error. See specs/domains/theming.md.
func themeSlug(filename string) (string, error) {
	base := strings.ToLower(filepath.Base(filename))
	base = strings.TrimSuffix(base, ".css")
	base = slugDisallowedRe.ReplaceAllString(base, "-")
	base = slugDashesRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		return "", fmt.Errorf("theme file name %q does not produce a valid theme id", filepath.Base(filename))
	}
	if _, reserved := reservedThemeIDs[base]; reserved {
		return "", fmt.Errorf("theme id %q is reserved for built-in themes", base)
	}
	return base, nil
}

// prettyThemeName prettifies a file name into a display name ("nord.css" →
// "Nord"); a name that normalizes to nothing falls back to "Theme".
// See specs/domains/theming.md.
func prettyThemeName(filename string) string {
	base := filepath.Base(filename)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	words := strings.Fields(base)
	if len(words) == 0 {
		return "Theme"
	}
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		r, size := utf8.DecodeRuneInString(w)
		b.WriteRune(unicode.ToUpper(r))
		b.WriteString(w[size:])
	}
	return b.String()
}
