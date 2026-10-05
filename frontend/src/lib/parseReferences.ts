// Reference parsing/formatting for user message text.

/**
 * The three `/`-ref kinds sharing one trigger (issue #110 + the MCP mention
 * flow): `agent` = Subagent Profile (threaded as `activeAgents` →
 * `## Requested Subagents`), `skill` = Agent Skill (threaded as
 * `activeSkills` → `## Active Skills`), `mcp` = MCP server (threaded as
 * `activeMCPServers` → the soft `## Requested MCP Servers` directive and
 * the manual-mode gating in core).
 */
export type RefKind = 'agent' | 'skill' | 'mcp'

export interface TypedRef {
  kind: RefKind
  /**
   * True when the ref was written in the collision-qualified form
   * (`/agent: name` / `/skill: name` / `/mcp: name`), whose kind is
   * explicit. False for a plain `/name`, whose kind is resolved against
   * the catalogs.
   */
  qualified: boolean
  name: string
}

// One `/`-trigger grammar for all three kinds, shared with display parsing.
// Boundaries use Go's isRefBoundarySpace set rather than JavaScript's wider
// \s class. Names are ASCII word characters/hyphens; a qualified marker
// accepts zero or one space/tab after its colon. The complete name MUST end
// at whitespace or EOF: paths, punctuation suffixes and malformed qualified
// tokens cannot backtrack into a shorter name or a plain marker ref.
export const REF_BOUNDARY_SPACE_SOURCE = String.raw`[ \t\n\r\f]`
// Use a strict EOF assertion: JavaScript's `$` also accepts a position before
// a final Unicode line separator, which Go does not treat as a boundary.
export const SLASH_REF_SOURCE = String.raw`\/(?:(agent|skill|mcp):[ \t]?([\w-]+)|([\w-]+))(?=${REF_BOUNDARY_SPACE_SOURCE}|(?![\s\S]))`
const REF_PATTERN = new RegExp(`(?:^|${REF_BOUNDARY_SPACE_SOURCE})${SLASH_REF_SOURCE}`, 'g')

/**
 * Extract every `/`-ref from the user message text as a typed ref.
 *
 * Extraction stays permissive and catalog-free (a plain `/word` is a
 * candidate ref regardless of catalog membership) so it can stay in sync
 * with the display splitter in userMessageSegments.ts; kind resolution and
 * collision detection live in {@link partitionRefs}. `#` no longer has any
 * ref meaning — it is not extracted here (historical `#foo` text is just
 * text), and the `#` inside `@file#L20` anchors belongs to the file ref.
 *
 * Duplicates are collapsed per spelling: two plain `/name` occurrences yield
 * one ref, while `/agent: name`, `/skill: name` and `/mcp: name` are distinct
 * (different explicit kinds).
 */
export function extractRefs(text: string): TypedRef[] {
  const refs: TypedRef[] = []
  const seen = new Set<string>()
  let match: RegExpExecArray | null

  REF_PATTERN.lastIndex = 0
  while ((match = REF_PATTERN.exec(text)) !== null) {
    const qualifiedKind = match[1]
    const qualifiedName = match[2]
    const plainName = match[3]
    let ref: TypedRef
    if (qualifiedKind !== undefined && qualifiedName !== undefined) {
      ref = { kind: qualifiedKind as RefKind, qualified: true, name: qualifiedName }
    } else if (plainName !== undefined) {
      ref = { kind: 'skill', qualified: false, name: plainName }
    } else {
      continue
    }
    const key = `${ref.qualified ? ref.kind : 'plain'}:${ref.name}`
    if (!seen.has(key)) {
      seen.add(key)
      refs.push(ref)
    }
  }
  return refs
}

export interface PartitionedRefs {
  /** Names to thread as `activeAgents` (→ `## Requested Subagents`). */
  agents: string[]
  /** Names to thread as `activeSkills` (→ `## Active Skills`). */
  skills: string[]
  /**
   * Names to thread as `activeMCPServers` (→ the soft
   * `## Requested MCP Servers` directive + manual-mode gating in core).
   */
  mcpServers: string[]
  /**
   * Plain `/name` refs that collide — the exact name is present in MORE THAN
   * ONE of the three catalogs — so their kind cannot be resolved. The caller
   * must treat these as ambiguous (no-op + hint toward the qualified
   * `/agent:` / `/skill:` / `/mcp:` spelling) instead of guessing.
   */
  ambiguous: string[]
}

/**
 * Partition extracted refs into the three send-path lists using the
 * discovered catalogs (issue #110 + the MCP mention flow):
 *
 *  - Qualified refs carry an explicit kind and are threaded verbatim — the
 *    qualified form is always valid input, no catalog check applies.
 *  - A plain ref whose name is in exactly one catalog is partitioned to that
 *    kind. A name in MORE THAN ONE catalog is a collision: reported via
 *    `ambiguous` for the caller's no-op handling, never guessed.
 *  - A plain name in NO catalog degrades to a skill ref — the historical
 *    permissive behavior (extractSkillRefs passed everything raw and the
 *    server resolves/skips unknown skills), preserved so an unavailable
 *    catalog (fetch failure) never silently drops the user's text.
 *
 * All output lists preserve first-occurrence order and are deduplicated
 * (a name reached via both a qualified and a plain spelling threads once).
 */
export function partitionRefs(refs: TypedRef[], agentNames: string[], skillNames: string[], mcpNames: string[] = []): PartitionedRefs {
  const agents = new Set<string>()
  const skills = new Set<string>()
  const mcpServers = new Set<string>()
  const agentCatalog = new Set(agentNames)
  const skillCatalog = new Set(skillNames)
  const mcpCatalog = new Set(mcpNames)
  const ambiguous: string[] = []
  const ambiguousSeen = new Set<string>()

  for (const ref of refs) {
    if (ref.qualified) {
      if (ref.kind === 'agent') agents.add(ref.name)
      else if (ref.kind === 'mcp') mcpServers.add(ref.name)
      else skills.add(ref.name)
      continue
    }
    const isAgent = agentCatalog.has(ref.name)
    const isSkill = skillCatalog.has(ref.name)
    const isMCP = mcpCatalog.has(ref.name)
    if (Number(isAgent) + Number(isSkill) + Number(isMCP) > 1) {
      if (!ambiguousSeen.has(ref.name)) {
        ambiguousSeen.add(ref.name)
        ambiguous.push(ref.name)
      }
    } else if (isAgent) {
      agents.add(ref.name)
    } else if (isMCP) {
      mcpServers.add(ref.name)
    } else {
      // Skill, or unknown in every catalog (permissive skill passthrough).
      skills.add(ref.name)
    }
  }
  return { agents: [...agents], skills: [...skills], mcpServers: [...mcpServers], ambiguous }
}

/**
 * Format a path for insertion into the chat input as an @file reference.
 *
 * Paths containing spaces are quoted (`@'my file.go'`, the canonical form)
 * unless the path itself contains a single quote — a quote cannot be nested
 * inside the quoting, so such paths fall back to backslash-escaped spaces
 * (`@my\ file.go`, the legacy form). Both forms are recognized by every
 * consumer (input highlighting, chat display, backend preprocessing), so
 * this only picks the more readable spelling for what gets typed.
 *
 * `forceQuoted` is used while completing inside an already-open `@'…' ref:
 * the replacement range swallows the typed opening quote, so the applied
 * text must reintroduce it even when the chosen path needs no quoting.
 */
export function formatFileRefPath(relPath: string, forceQuoted = false): string {
  if ((forceQuoted || relPath.includes(' ')) && !relPath.includes("'")) {
    return `'${relPath}'`
  }
  return relPath.replace(/ /g, '\\ ')
}
