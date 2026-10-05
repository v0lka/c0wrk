package core

import (
	"path"
	"regexp"
	"strings"
)

// fileRefPattern matches @path references in either form: a single-quoted
// path (@'my file.go', the canonical form for paths with spaces) or a bare
// path with optional backslash-escaped spaces (@my\ file.go, the legacy
// form). An optional line/line-range anchor may follow; the anchor accepts
// GitHub-style forms: #N, #N-M (legacy bare-number) and #LN, #LN-LN (e.g.
// #L20-L36). The quoted alternative must come first so a quoted ref is
// consumed as one token instead of falling through to the bare-path alt.
var fileRefPattern = regexp.MustCompile(`(?:^|\s)@((?:'[^']+'|(?:[^\s\\]|\\.)+)(?:#L?\d+(?:-L?\d+)?)?)`)

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
//  1. Strips /-mention references for skills, subagent profiles, and MCP
//     servers (ADR-076): the plain form "/name" — stripped when the name
//     appears in activeSkills, activeAgents, OR activeMCPServers (the three
//     catalogs sharing one trigger) — and the collision-qualified forms
//     "/agent: id", "/skill: id", and "/mcp: id", with or without a space
//     after the colon (the spaced form is what the dropdown inserts
//     canonically).
//  2. Converts @file-path references to fileref:// URIs, resolving each
//     relative path against workspacePath so the LLM receives unambiguous
//     absolute paths. Both the quoted (@'my file.go') and the legacy
//     backslash-escaped (@my\ file.go) forms are recognized. Absolute and
//     home-relative (~/...) paths, and refs when workspacePath is empty, are
//     left unchanged.
//
// Stripping is catalog-gated and fail-closed: only names the caller threaded
// (the send path extracted them from the text and partitioned them against
// the catalogs) are removed; any other /-token is preserved verbatim.
// "#" is no longer a mention trigger — "#foo" is always plain text now, and
// the "#" in a GitHub-style line anchor like @file#L20 belongs to the file
// ref either way.
//
// The leading "/goal" command is not special-cased here: "goal" is a command
// name, not a catalog name, so a "/goal …" prefix survives preprocessing and
// DetectAndStripGoalMode — which runs on the processed text — still sees it,
// including the case where a stripped ref ahead of it ("/explore /goal …")
// exposes the command at the start of the processed text.
func PreprocessMessageText(text string, activeSkills, activeAgents, activeMCPServers []string, workspacePath string) string {
	// Strip /-mention references (plain + qualified, skills ∪ agents ∪ MCP servers).
	result := stripMentionRefs(text, activeSkills, activeAgents, activeMCPServers)

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
		// Normalize the path form. A trailing GitHub-style line anchor
		// (#N, #L20-L36, …) is split off first so it survives both the
		// unquoting of the @'…' form and the unescaping of the legacy
		// backslash-escaped form, and is re-attached unchanged.
		path = normalizeFileRefPath(path)
		// Resolve to an absolute path relative to the workspace.
		path = resolveFileRefPath(path, workspacePath)
		return prefix + "fileref://" + path
	})

	// Collapse multiple spaces into one.
	result = multiSpaceRe.ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}

// Qualified-mention markers (ADR-076): "/agent:", "/skill:", and "/mcp:"
// prefix a collision-qualified mention whose id follows the colon, with or
// without a space ("/agent: code-reviewer", "/agent:code-reviewer"). The
// markers are case-sensitive, like every name lookup here — a collision is an
// exact, case-sensitive name match (issue #110).
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

// stripMentionRefs removes every /-mention reference from text in a single
// left-to-right pass: plain "/name" for names in activeSkills ∪ activeAgents
// ∪ activeMCPServers and qualified "/agent: id" / "/skill: id" / "/mcp: id"
// (spaced or unspaced colon) for ids in the respective catalog. Surrounding
// whitespace is left in place — PreprocessMessageText's multi-space collapse
// and final trim reduce it — so stripping preserves the prose exactly like
// the former per-name regexes. With all three catalogs empty the text is
// returned unchanged (fail-closed: an unknown or unthreaded mention is never
// stripped).
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
// (which the caller has already verified is a '/' at a token boundary) and
// returns the index just past it. It parses exactly one candidate:
//
//   - Qualified: "/agent: id", "/skill: id", or "/mcp: id" — an optional
//     single space or tab after the colon (the canonical dropdown emission
//     uses the space), then the id, which must end at whitespace or
//     end-of-text. The id is looked up in the marker's own catalog; a
//     mismatched catalog or an unknown id is preserved verbatim.
//   - Plain: "/name" — the name must end at whitespace or end-of-text and
//     match any catalog (skills ∪ agents ∪ mcp servers; the kind distinction
//     is the send path's partitioning job, the text strip is kind-agnostic).
//
// A "/" that starts no strippable mention — including "/agent:" with a
// missing, non-boundary, or unknown id — returns ok=false and the caller
// emits the byte verbatim. The ":" of a qualified attempt also breaks the
// plain form's trailing boundary, so no partial "/agent" fragment of
// "/agent: id" can ever be stripped by the plain path.
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

// normalizeFileRefPath normalizes the path portion of an @file reference to
// its literal form. Two input forms are supported:
//
//   - Single-quoted (@'my file.go'): the content between the quotes is taken
//     verbatim — spaces need no escaping and backslashes are literal.
//   - Bare with backslash-escaped spaces (@my\ file.go, the legacy form):
//     each `\ ` escape is unescaped to a plain space.
//
// A trailing GitHub-style line anchor (#N, #N-M, #LN, #LN-LN) — which may
// sit after the closing quote (@'f.go'#L20) or inside it (@'f.go#L20') — is
// split off before normalization and re-attached verbatim, so it never
// interferes with quote detection or path resolution.
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
// The detection runs on the already-preprocessed text (after /-mention
// stripping and @file conversion) so a "/goal /skill-name …" invocation still
// works: the mention ref is stripped first, then /goal is detected. An empty
// remainder after stripping (e.g. a bare "/goal") is returned empty with
// isGoal=true — the orchestrator's deriveGoal will ground the goal from the
// (empty) message.
func DetectAndStripGoalMode(text string) (cleaned string, isGoal bool) {
	if !goalModePrefixRe.MatchString(text) {
		return text, false
	}
	cleaned = goalModePrefixRe.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned), true
}
