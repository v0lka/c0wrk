package core

import (
	"path"
	"regexp"
	"strings"
)

// fileRefPattern matches @path references (with optional backslash-escaped spaces
// and an optional line/line-range anchor). The anchor accepts GitHub-style forms:
// #N, #N-M (legacy bare-number) and #LN, #LN-LN (e.g. #L20-L36).
var fileRefPattern = regexp.MustCompile(`(?:^|\s)@((?:[^\s\\]|\\.)+(?:#L?\d+(?:-L?\d+)?)?)`)

// lineAnchorSuffixRe matches a trailing GitHub-style line/line-range anchor
// (#N, #N-M, #LN, #LN-LN) at the end of an @file reference path. Unlike an
// optional-anchored pattern it only matches when an actual anchor is present,
// so FindStringIndex returns nil for plain paths and a real match otherwise.
// It is used to split the anchor off the path so the path portion can be
// resolved to an absolute form while the anchor is re-attached verbatim.
var lineAnchorSuffixRe = regexp.MustCompile(`#L?\d+(?:-L?\d+)?$`)

// multiSpaceRe collapses runs of 2+ spaces into one.
var multiSpaceRe = regexp.MustCompile(`  +`)

// PreprocessMessageText transforms a user message for the orchestrator:
//  1. Strips /skill-name references for each skill in activeSkills.
//  2. Strips #agent-name references for each agent in activeAgents.
//  3. Converts @file-path references to fileref:// URIs, resolving each
//     relative path against workspacePath so the LLM receives unambiguous
//     absolute paths. Absolute and home-relative (~/...) paths, and refs
//     when workspacePath is empty, are left unchanged.
//
// Only known agent names (from activeAgents) are stripped — exactly mirroring
// /skill stripping — so a GitHub-style line anchor like @file#L20 is never
// touched (the "#" there is glued to the path with no preceding whitespace,
// and "L20" is not a known agent name regardless).
func PreprocessMessageText(text string, activeSkills, activeAgents []string, workspacePath string) string {
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

	// Strip agent references (#agent-name). Mirrors /skill stripping: only
	// explicitly mentioned agent names are removed, preserving surrounding
	// whitespace boundaries.
	for _, name := range activeAgents {
		pattern := regexp.MustCompile(`(?:^|\s)#` + regexp.QuoteMeta(name) + `(?:\s|$)`)
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
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
		// Resolve to an absolute path relative to the workspace. A trailing
		// GitHub-style line anchor (#N, #L20-L36, …) must be split off so it
		// is not mistaken for a path component; it is re-attached unchanged.
		path = resolveFileRefPath(path, workspacePath)
		return prefix + "fileref://" + path
	})

	// Collapse multiple spaces into one.
	result = multiSpaceRe.ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}

// resolveFileRefPath resolves an @file reference's path portion against the
// workspace root so the LLM prompt contains unambiguous absolute paths.
//
// The path may carry a trailing GitHub-style line anchor (#N, #N-M, #LN,
// #LN-LN) which is split off before resolution and re-attached verbatim.
//
// Resolution rules:
//   - If workspacePath is empty, the path is returned unchanged.
//   - Absolute paths (leading "/") and home-relative paths (leading "~/") are
//     left as-is — they are already unambiguous.
//   - All other (relative) paths are joined with workspacePath and cleaned.
//
// The result is the path component of a fileref:// URI, which is a logical
// identifier rather than an OS filesystem path. The POSIX-oriented path
// package is therefore used instead of filepath: it always emits forward
// slashes and is platform-independent, whereas filepath.Join would produce
// OS-native separators (backslashes on Windows) and filepath.IsAbs would
// reject Unix-style absolute paths on Windows — both breaking the URI.
func resolveFileRefPath(refPath, workspacePath string) string {
	if workspacePath == "" {
		return refPath
	}
	// Split a trailing line anchor (#N, #L20-L36, …) so path.Join does
	// not treat it as a path component.
	anchor := ""
	if loc := lineAnchorSuffixRe.FindStringIndex(refPath); loc != nil {
		anchor = refPath[loc[0]:]
		refPath = refPath[:loc[0]]
	}
	if refPath == "" {
		return anchor
	}
	// Leave already-absolute and home-relative paths untouched.
	if strings.HasPrefix(refPath, "/") || strings.HasPrefix(refPath, "~") {
		return refPath + anchor
	}
	// path.Join cleans dot segments (./, ../) and joins with a forward slash,
	// yielding a stable URI path component on every platform.
	return path.Join(workspacePath, refPath) + anchor
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
