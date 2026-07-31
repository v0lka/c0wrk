// Skill reference extraction from user message text.

const SKILL_REF_PATTERN = /(?:^|\s)\/([\w-]+)/g

/**
 * Extract skill names referenced via /skill-name patterns in the user message.
 * Only matches when preceded by whitespace or start-of-string.
 */
export function extractSkillRefs(text: string): string[] {
  const skills: string[] = []
  const seen = new Set<string>()
  let match: RegExpExecArray | null

  SKILL_REF_PATTERN.lastIndex = 0
  while ((match = SKILL_REF_PATTERN.exec(text)) !== null) {
    const name = match[1]
    if (name === undefined) continue
    if (!seen.has(name)) {
      seen.add(name)
      skills.push(name)
    }
  }
  return skills
}

// Agent reference extraction from user message text. Mirrors SKILL_REF_PATTERN
// but uses '#' as the trigger. A '#' glued to an @file path (e.g. @x.go#L20) is
// never matched: there the '#' has no preceding whitespace and is part of the
// file token.
const AGENT_REF_PATTERN = /(?:^|\s)#([\w-]+)/g

/**
 * Extract agent names referenced via #agent-name patterns in the user message.
 * Only matches when preceded by whitespace or start-of-string.
 *
 * Extraction is intentionally permissive — any `#word` is a candidate — because
 * the catalog membership check lives in {@link filterKnownAgentRefs}. Keeping
 * extraction pure (no catalog knowledge) lets it stay in sync with the display
 * splitter in userMessageSegments.ts.
 */
export function extractAgentRefs(text: string): string[] {
  const agents: string[] = []
  const seen = new Set<string>()
  let match: RegExpExecArray | null

  AGENT_REF_PATTERN.lastIndex = 0
  while ((match = AGENT_REF_PATTERN.exec(text)) !== null) {
    const name = match[1]
    if (name === undefined) continue
    if (!seen.has(name)) {
      seen.add(name)
      agents.push(name)
    }
  }
  return agents
}

/**
 * Filter agent refs down to those matching a known Subagent Profile name.
 *
 * Extraction ({@link extractAgentRefs}) is permissive by design, but only
 * `#mentions` of *real* profiles should be threaded (→ `activeAgents`), stripped
 * from the message text (→ `PreprocessMessageText`), or badged. Without this
 * filter, common coding-domain prose would be misinterpreted as agent refs:
 *
 *  - `#42` / `#123` — GitHub issue/PR references
 *  - `#L100` — bare line refs
 *  - `#refactor` — hashtags
 *
 * A misinterpreted mention is stripped from the user's message (data loss) and
 * injected as a `## Requested Subagents` directive for a nonexistent agent
 * (prompt noise). Filtering against the discovered catalog — the same catalog
 * the `#`-autocomplete already uses — keeps extraction, completion, and the
 * backend directive consistent. An empty `knownNames` (no profiles, or catalog
 * unavailable) yields an empty result, which is the safe degradation: no
 * mention is threaded or stripped, so the message text is preserved verbatim.
 */
export function filterKnownAgentRefs(refs: string[], knownNames: string[]): string[] {
  const known = new Set(knownNames)
  return refs.filter((name) => known.has(name))
}
