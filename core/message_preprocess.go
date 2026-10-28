package core

import (
	"path"
	"regexp"
	"strings"
)

// fileRefPattern matches @path references in either form: a single-quoted
// path (@'my file.go') or a bare path with backslash-escaped spaces (@my\
// file.go, legacy), plus an optional GitHub-style line anchor (#N, #N-M,
// #LN, #LN-LN). The quoted alternative must come first so a quoted ref is
// consumed as one token instead of falling through to the bare-path alt.
var fileRefPattern = regexp.MustCompile(`(?:^|\s)@((?:'[^']+'|(?:[^\s\\]|\\.)+)(?:#L?\d+(?:-L?\d+)?)?)`)

// lineAnchorSuffixRe matches a trailing GitHub-style line/line-range anchor
// (#N, #N-M, #LN, #LN-LN) at the end of an @file reference: unlike an
// optional-anchored pattern it only matches when an anchor is actually
// present, so it cleanly splits the anchor off the path.
var lineAnchorSuffixRe = regexp.MustCompile(`#L?\d+(?:-L?\d+)?$`)

// multiSpaceRe collapses runs of 2+ spaces into one.
var multiSpaceRe = regexp.MustCompile(`  +`)

// PreprocessMessageText transforms a user message for the orchestrator:
// strips /-mention references (plain + collision-qualified, the three
// catalogs sharing one trigger) and converts @file-path references to
// fileref:// URIs resolved against workspacePath. The mention grammar and the
// fail-closed, catalog-gated strip: specs/decisions/076-unified-slash-mentions.md.
// The path resolution rules (quoted/legacy-escaped forms, POSIX/~ vs
// Windows-dialect absolutes, relative-joins-workspace):
// specs/decisions/095-chat-file-reference-path-semantics.md.
//
// "/goal" is deliberately not special-cased here: "goal" is a command name,
// not a catalog name, so a "/goal …" prefix survives preprocessing and
// DetectAndStripGoalMode — which runs on the processed text — still sees it,
// including when a stripped ref ahead of it exposes the command at the start.
func PreprocessMessageText(text string, activeSkills, activeAgents, activeMCPServers []string, workspacePath string) string {
	result := stripMentionRefs(text, activeSkills, activeAgents, activeMCPServers)

	result = fileRefPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Preserve leading whitespace.
		prefix := ""
		trimmed := match
		if trimmed != "" && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n') {
			prefix = trimmed[:1]
			trimmed = trimmed[1:]
		}
		path := strings.TrimPrefix(trimmed, "@")
		path = normalizeFileRefPath(path)
		path = resolveFileRefPath(path, workspacePath)
		return prefix + "fileref://" + path
	})

	result = multiSpaceRe.ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}

// Qualified-mention markers (ADR-076): "/agent:", "/skill:", "/mcp:" prefix a
// collision-qualified mention, with or without a space after the colon. Names
// are matched case-sensitively (a collision is an exact name match, issue #110).
const (
	qualifiedAgentMarker = "agent"
	qualifiedSkillMarker = "skill"
	qualifiedMCPMarker   = "mcp"
)

// isMentionNameByte reports whether c can appear in a mention token. Skill
// and agent names are lowercase alphanumeric with hyphens (agentskills.io
// spec, agentNamePattern); the wider scan set (uppercase, underscore) merely
// tolerates hand-typed tokens, which then fail the exact catalog lookup.
func isMentionNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// isRefBoundarySpace matches the ASCII whitespace class used by mention
// extraction and display (REF_BOUNDARY_SPACE_SOURCE in parseReferences.ts):
// tab, newline, form feed, carriage return, space. This is RE2's \s set,
// not JavaScript's wider Unicode-aware \s set.
func isRefBoundarySpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// stripMentionRefs removes every strippable /-mention in one left-to-right
// pass (specs/decisions/076-unified-slash-mentions.md: the single-scanner
// grammar). Whitespace is left in place — the multi-space collapse and final
// trim reduce it; with all catalogs empty the text is returned unchanged.
func stripMentionRefs(text string, activeSkills, activeAgents, activeMCPServers []string) string {
	if len(activeSkills) == 0 && len(activeAgents) == 0 && len(activeMCPServers) == 0 {
		return text
	}
	skills := make(map[string]struct{}, len(activeSkills))
	for _, name := range activeSkills {
		skills[name] = struct{}{}
	}
	agents := make(map[string]struct{}, len(activeAgents))
	for _, name := range activeAgents {
		agents[name] = struct{}{}
	}
	mcpServers := make(map[string]struct{}, len(activeMCPServers))
	for _, name := range activeMCPServers {
		mcpServers[name] = struct{}{}
	}

	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '/' && (i == 0 || isRefBoundarySpace(text[i-1])) {
			if end, ok := mentionRefEnd(text, i, skills, agents, mcpServers); ok {
				i = end
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// mentionRefEnd reports whether a strippable /-mention starts at text[i]
// (a '/' at a token boundary) and returns the index just past it. It parses
// exactly one candidate — qualified "/agent: id" / "/skill: id" / "/mcp: id"
// (optional space/tab, id ending at a boundary, looked up in the marker's own
// catalog) or plain "/name" (exact membership in any catalog; the kind
// distinction is the send path's partitioning job). ok=false leaves the byte
// verbatim; the ":" of a qualified attempt also breaks the plain form's
// trailing boundary, so no partial "/agent" fragment of "/agent: id" can
// ever be stripped by the plain path.
func mentionRefEnd(text string, i int, skills, agents, mcpServers map[string]struct{}) (int, bool) {
	j := i + 1
	for j < len(text) && isMentionNameByte(text[j]) {
		j++
	}
	if j == i+1 {
		return i, false // "/" with no token
	}
	token := text[i+1 : j]

	// Collision-qualified form: the marker must be followed directly by ":".
	if token == qualifiedAgentMarker || token == qualifiedSkillMarker || token == qualifiedMCPMarker {
		if j < len(text) && text[j] == ':' {
			k := j + 1
			if k < len(text) && (text[k] == ' ' || text[k] == '\t') {
				k++
			}
			m := k
			for m < len(text) && isMentionNameByte(text[m]) {
				m++
			}
			if m > k && (m == len(text) || isRefBoundarySpace(text[m])) {
				id := text[k:m]
				catalog := agents
				switch token {
				case qualifiedSkillMarker:
					catalog = skills
				case qualifiedMCPMarker:
					catalog = mcpServers
				}
				if _, ok := catalog[id]; ok {
					return m, true
				}
			}
			return i, false
		}
		// No colon: fall through to the plain path (a profile, skill, or MCP
		// server literally named "agent"/"skill"/"mcp" strips as a plain
		// mention).
	}

	// Plain form: exact, case-sensitive membership in any catalog, with
	// the token ending at a whitespace boundary or end-of-text.
	if j == len(text) || isRefBoundarySpace(text[j]) {
		if _, ok := skills[token]; ok {
			return j, true
		}
		if _, ok := agents[token]; ok {
			return j, true
		}
		if _, ok := mcpServers[token]; ok {
			return j, true
		}
	}
	return i, false
}

// normalizeFileRefPath normalizes an @file reference's path portion to its
// literal form: quoted content verbatim, legacy `\ ` escapes unescaped, and a
// trailing GitHub-style line anchor split off and re-attached verbatim.
// specs/decisions/095-chat-file-reference-path-semantics.md.
func normalizeFileRefPath(refPath string) string {
	anchor := ""
	if loc := lineAnchorSuffixRe.FindStringIndex(refPath); loc != nil {
		anchor = refPath[loc[0]:]
		refPath = refPath[:loc[0]]
	}
	if len(refPath) >= 2 && strings.HasPrefix(refPath, "'") && strings.HasSuffix(refPath, "'") {
		return refPath[1:len(refPath)-1] + anchor
	}
	return strings.ReplaceAll(refPath, `\ `, " ") + anchor
}

// resolveFileRefPath resolves an @file reference's path portion so the LLM
// prompt carries unambiguous absolute paths. Resolution rules (POSIX/~ left
// as-is, Windows drive/UNC canonicalized to forward slashes and never joined
// with the workspace, relative joined with workspacePath) and the rationale
// for the POSIX path package over filepath (a fileref:// URI is a logical
// identifier, not an OS path):
// specs/decisions/095-chat-file-reference-path-semantics.md.
func resolveFileRefPath(refPath, workspacePath string) string {
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
	// Leave already-absolute (POSIX) and home-relative paths untouched.
	if strings.HasPrefix(refPath, "/") || strings.HasPrefix(refPath, "~") {
		return refPath + anchor
	}
	// Windows drive-rooted and UNC paths are absolute in their own dialect:
	// canonical forward-slash form, never joined with the workspace.
	if IsAbsolutePath(refPath) {
		return NormalizeAbsPath(refPath) + anchor
	}
	// Relative paths need the workspace; without one there is nothing to
	// resolve against and the path is returned unchanged.
	if workspacePath == "" {
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

// DetectAndStripGoalMode inspects a (preprocessed) message for a leading
// "/goal" command — one of goal mode's two explicit entry points (specs/domains/goal-mode.md) —
// returning it stripped (trimmed) with isGoal=true. It runs on the
// already-preprocessed text so "/goal /skill-name …" still works (ADR-076); an
// empty remainder (bare "/goal") returns empty with isGoal=true — deriveGoal
// grounds the goal from the (empty) message.
func DetectAndStripGoalMode(text string) (cleaned string, isGoal bool) {
	if !goalModePrefixRe.MatchString(text) {
		return text, false
	}
	cleaned = goalModePrefixRe.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned), true
}
