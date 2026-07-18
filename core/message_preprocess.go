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

// goalModePrefixRe matches a leading "/goal" command (optionally followed by
// whitespace) that selects goal mode for the first message of a task. It must
// be anchored at the start of the (trimmed) message and be followed by either
// whitespace or end-of-string so it does not false-match "/goals-report".
var goalModePrefixRe = regexp.MustCompile(`^/goal(?:\s+|$)`)

// DetectAndStripGoalMode inspects a (preprocessed) user message for a leading
// "/goal" command. When present, it returns the message with the command
// stripped (trimmed) and isGoal=true; the caller sets HandleOptions.Goal so
// HandleMessage dispatches to the multi-turn goal loop. When absent, the
// message is returned unchanged and isGoal=false.
//
// The detection runs on the already-preprocessed text (after /skill stripping
// and @file conversion) so a "/goal /skill-name …" invocation still works:
// the /skill ref is stripped first, then /goal is detected. An empty remainder
// after stripping (e.g. a bare "/goal") is returned empty with isGoal=true —
// the orchestrator's deriveGoal will ground the goal from the (empty) message.
func DetectAndStripGoalMode(text string) (cleaned string, isGoal bool) {
	if !goalModePrefixRe.MatchString(text) {
		return text, false
	}
	cleaned = goalModePrefixRe.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned), true
}
