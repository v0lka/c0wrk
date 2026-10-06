package backend

import (
	"sort"
	"strings"
)

// parseFontFamilies converts raw `fc-list --format '%{family}\n'` output into
// the deduplicated list of installed font family names, sorted
// case-insensitively. It is platform-neutral (pure string work) so the whole
// pipeline is testable on every build target; only the reader that produces
// the raw text is platform-specific (fontlist_linux.go / fontlist_other.go).
//
// fc-list prints one line per font file, and `%{family}` expands to that
// file's family aliases joined by commas (e.g. "DejaVu Sans,DejaVu Sans
// Book"): a single physical family shows up under several aliases, and the
// same alias repeats across styles and weights. The pipeline therefore
// splits every line on commas, trims each alias, drops empties, and
// deduplicates case-insensitively keeping the FIRST spelling seen (fc-list's
// own canonical casing wins over a later all-caps repeat). The result is
// sorted case-insensitively for a stable picker order that ignores case;
// after the case-insensitive dedup no two remaining names collide under
// that comparison, so the order is fully deterministic.
//
// The result is never nil — an empty input yields an empty (non-nil) slice
// so the wire response serializes as [] rather than null.
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
