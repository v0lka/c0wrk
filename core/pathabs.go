package core

import (
	"regexp"
	"strings"
)

// absolutePathRe matches a GOOS-independent absolute path (mirroring
// isAbsolutePath in frontend/src/lib/localFileLink.ts): POSIX "/", drive
// "^[A-Za-z]:[\\/]" ("C:x" stays relative), UNC. See specs/decisions/095-chat-file-reference-path-semantics.md.
var absolutePathRe = regexp.MustCompile(`^(?:/|[A-Za-z]:[\\/]|[\\/]{2})`)

// IsAbsolutePath reports whether p is absolute in any dialect an @file reference
// may carry — purely lexical (no fs access, no OS dependency), so the same text
// classifies identically on every platform (the fileref URI is a logical
// identifier); "~" is deliberately not absolute. See specs/decisions/095-chat-file-reference-path-semantics.md.
func IsAbsolutePath(p string) bool {
	return absolutePathRe.MatchString(p)
}

// NormalizeAbsPath returns the canonical forward-slash form of an absolute
// path for a fileref:// URI (POSIX /a/b/c, drive C:/a/b/c, UNC
// //server/share/a/b): backslashes become slashes, dot segments are removed,
// ".." clamped at the anchored root — never a plain path.Clean, which pops
// across the anchor ("C:\..\x" → "../x", erasing the drive) and collapses the
// UNC "//" marker. Mirrors normalizeAbsolute in frontend/src/lib/localFileLink.ts.
// See specs/decisions/095-chat-file-reference-path-semantics.md.
func NormalizeAbsPath(p string) string {
	s := strings.ReplaceAll(p, "\\", "/")
	switch {
	case strings.HasPrefix(s, "//"):
		// UNC: the first two segments (server, share) are pinned.
		return "//" + cleanAbsSegments(s[2:], 2)
	case len(s) >= 2 && s[1] == ':' && isDriveLetterByte(s[0]):
		// Drive-rooted: the "X:" prefix is the anchor; the canonical form
		// has exactly one "/" after the colon.
		return s[:2] + "/" + cleanAbsSegments(s[2:], 0)
	case strings.HasPrefix(s, "/"):
		// POSIX: the leading slash is the anchor.
		return "/" + cleanAbsSegments(s[1:], 0)
	}
	// Not absolute — clean as far as a relative path allows (total for direct callers).
	return cleanAbsSegments(s, 0)
}

// isDriveLetterByte reports whether b is an ASCII letter — the drive byte of
// a Windows drive-rooted path.
func isDriveLetterByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// cleanAbsSegments removes empty and "." segments and resolves ".." against
// the remainder, never popping below the floor segments (the anchored root).
// Mirrors normalizeSegments in frontend/src/lib/localFileLink.ts.
func cleanAbsSegments(tail string, floor int) string {
	parts := strings.Split(tail, "/")
	segs := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			// Drop empty and current-dir segments.
		case "..":
			if len(segs) > floor {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, part)
		}
	}
	return strings.Join(segs, "/")
}
