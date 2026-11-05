package backend

import (
	"sort"
	"strings"
)

// parseFontFamilies converts raw `fc-list --format '%{family}\n'` output into the
// deduplicated installed-family list: comma-joined aliases per line are split, trimmed,
// deduplicated case-insensitively keeping the FIRST spelling, sorted case-insensitively;
// never nil. Pure string work, testable on every target. See specs/domains/fonts.md.
func parseFontFamilies(raw string) []string {
	lines := strings.Split(raw, "\n")
	families := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		for _, alias := range strings.Split(line, ",") {
			name := strings.TrimSpace(alias)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			families = append(families, name)
		}
	}
	sort.Slice(families, func(i, j int) bool {
		return strings.ToLower(families[i]) < strings.ToLower(families[j])
	})
	return families
}
