package session

import (
	"net/url"
	"strings"
)

// parseFileURIList parses a text/uri-list or x-special/gnome-copied-files
// payload (as produced by wl-paste / xclip) into filesystem paths: the GNOME
// "copy"/"cut" header is ignored; non-file:// and unparseable lines are
// skipped silently, source order preserved. Pure string transformation, no
// system dependency — kept build-tag-free, unit-testable on every platform.
func parseFileURIList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		// The GNOME format starts with a "copy"/"cut" operation header.
		if line == "" || line == "copy" || line == "cut" {
			continue
		}
		if !strings.HasPrefix(line, "file://") {
			continue
		}
		u, perr := url.Parse(line)
		if perr != nil {
			continue
		}
		// url.Parse already percent-decodes the path into u.Path; unescaping
		// again would corrupt names with a literal "%" (e.g. "100%25.txt").
		if u.Path != "" {
			paths = append(paths, u.Path)
		}
	}
	return paths
}
