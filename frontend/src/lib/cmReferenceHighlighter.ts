import { Decoration, EditorView, type DecorationSet } from '@codemirror/view'
import { StateField, RangeSetBuilder, type Text } from '@codemirror/state'
import { REF_BOUNDARY_SPACE_SOURCE, SLASH_REF_SOURCE, type RefKind } from './parseReferences'

// Slash refs ride the SAME grammar as the send path and the chat splitter
// (parseReferences.SLASH_REF_SOURCE): a collision-qualified `/agent: name` /
// `/mcp: name` / `/skill: name` keeps its explicit kind, so the editor paints
// exactly the color the autocomplete list row and the chat chip use, and a
// plain `/name` falls back to the skill tint (the permissive default of both
// the send and display paths). The leading boundary is a zero-width lookbehind
// so the preceding space never carries the mark.
const SLASH_REF_RE = new RegExp(
  String.raw`(?:^|(?<=${REF_BOUNDARY_SPACE_SOURCE}))${SLASH_REF_SOURCE}`,
  'g',
)
// Matches #agent-name preceded by whitespace or at line start. The `#`
// trigger was retired (ADR-076): this only keeps coloring historical spellings
// already present in old drafts/transcripts, matching the agent tint.
const AGENT_RE = /(?:^|(?<=\s))#([\w-]+)/g
// Matches @file-path refs preceded by whitespace or at line start. Two forms:
// a single-quoted path (@'my file.go', the canonical form for paths with
// spaces) or a bare path with backslash-escaped spaces (@my\ file.go, the
// legacy form), both with an optional #line anchor. The quoted alternative
// must come first so a quoted ref is consumed as one token.
const FILE_RE = /(?:^|(?<=\s))@(?:'[^']+'|(?:[^\s\\]|\\.)+)(?:#\d+(?:-\d+)?)?/g

const skillMark = Decoration.mark({ class: 'cm-ref-skill' })
const agentMark = Decoration.mark({ class: 'cm-ref-agent' })
const mcpMark = Decoration.mark({ class: 'cm-ref-mcp' })
const fileMark = Decoration.mark({ class: 'cm-ref-file' })

/** The mark for an explicit /-ref kind; a plain /name defaults to skill. */
function markForKind(kind: RefKind | undefined): Decoration {
  return kind === 'agent' ? agentMark : kind === 'mcp' ? mcpMark : skillMark
}

function buildDecorations(doc: Text): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>()
  const text = doc.toString()

  const matches: Array<{ start: number; end: number; deco: Decoration }> = []

  SLASH_REF_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = SLASH_REF_RE.exec(text)) !== null) {
    // Capture group 1 is the qualified kind (`agent`/`skill`/`mcp`); it is
    // undefined for a plain `/name`, which carries no explicit kind.
    matches.push({ start: m.index, end: m.index + m[0].length, deco: markForKind(m[1] as RefKind | undefined) })
  }

  AGENT_RE.lastIndex = 0
  while ((m = AGENT_RE.exec(text)) !== null) {
    matches.push({ start: m.index, end: m.index + m[0].length, deco: agentMark })
  }

  FILE_RE.lastIndex = 0
  while ((m = FILE_RE.exec(text)) !== null) {
    matches.push({ start: m.index, end: m.index + m[0].length, deco: fileMark })
  }

  // DecorationSet requires sorted, non-overlapping ranges.
  matches.sort((a, b) => a.start - b.start || a.end - b.end)
  for (const { start, end, deco } of matches) {
    builder.add(start, end, deco)
  }

  return builder.finish()
}

/**
 * StateField that highlights /skill, /agent, /mcp and @file reference tokens
 * in the editor, each in its kind's color. Using a StateField (instead of a
 * ViewPlugin) ensures decorations update atomically with document changes,
 * preventing cursor positioning lag.
 */
export const referenceHighlighter = StateField.define<DecorationSet>({
  create(state) {
    return buildDecorations(state.doc)
  },
  update(decorations, tr) {
    if (tr.docChanged) {
      return buildDecorations(tr.newDoc)
    }
    return decorations
  },
  provide: (f) => EditorView.decorations.from(f),
})
