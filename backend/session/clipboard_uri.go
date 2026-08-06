package session

import (
	"net/url"
	"strings"
)

// parseFileURIList parses a text/uri-list or x-special/gnome-copied-files
// payload (as produced by wl-paste / xclip on Linux) into filesystem paths.
//
// It is a pure string transformation with no system dependency, so it is kept
// build-tag-free (and therefore unit-testable on every platform) even though
// the only caller, clipboardFiles in clipboard_linux.go, is Linux-specific.
// Build tags gate clipboard *probing* (which touches the system), not parsing.
//
// Accepted shapes:
//
//   - text/uri-list: one "file://" URI per line.
//   - x-special/gnome-copied-files: a leading "copy"/"cut" operation header
//     followed by one "file://" URI per line (the header is ignored).
//
// Non-file:// lines and lines that fail to URL-parse/unescape are skipped
// silently. Returns the decoded filesystem paths in source order (empty slice
// when no file URI is present).
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
		p, derr := url.PathUnescape(u.Path)
		if derr != nil {
			continue
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
