package core

import (
	"regexp"
	"strings"
)

// fileRefPattern matches @path references (with optional backslash-escaped spaces and #LN or #LN-M suffix).
var fileRefPattern = regexp.MustCompile(`(?:^|\s)@((?:[^\s\\]|\\.)+(?:#\d+(?:-\d+)?)?)`)

// multiSpaceRe collapses runs of 2+ spaces into one.
var multiSpaceRe = regexp.MustCompile(`  +`)

// PreprocessMessageText transforms a user message for the orchestrator:
// 1. Strips /skill-name references for each skill in activeSkills.
// 2. Converts @file-path references to fileref:// URIs.
func PreprocessMessageText(text string, activeSkills []string) string {
	result := text

	// Strip skill references.
	for _, name := range activeSkills {
		pattern := regexp.MustCompile(`(?:^|\s)/` + regexp.QuoteMeta(name) + `(?:\s|$)`)
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			// Preserve surrounding whitespace boundaries: if the match had leading/trailing space,
			// collapse to a single space; if it was at start/end, remove entirely.
			leading := match != "" && match[0] == ' '
			trailing := match != "" && match[len(match)-1] == ' '
			if leading || trailing {
				return " "
			}
			return ""
		})
	}

	// Convert @file references to fileref:// URIs.
	result = fileRefPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Preserve leading whitespace.
		prefix := ""
		trimmed := match
		if trimmed != "" && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n') {
			prefix = trimmed[:1]
			trimmed = trimmed[1:]
		}
		// Remove the @ prefix.
		path := strings.TrimPrefix(trimmed, "@")
		// Unescape backslash-escaped spaces.
		path = strings.ReplaceAll(path, `\ `, " ")
		return prefix + "fileref://" + path
	})

	// Collapse multiple spaces into one.
	result = multiSpaceRe.ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}
