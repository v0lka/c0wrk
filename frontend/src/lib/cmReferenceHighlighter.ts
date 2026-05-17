import { Decoration, EditorView, type DecorationSet } from '@codemirror/view'
import { StateField, RangeSetBuilder, type Text } from '@codemirror/state'

// Matches /skill-name preceded by whitespace or at line start.
const SKILL_RE = /(?:^|(?<=\s))\/([\w-]+)/g
// Matches @file-path (with escaped spaces and optional #line) preceded by whitespace or at line start.
const FILE_RE = /(?:^|(?<=\s))@(?:[^\s\\]|\\.)+(?:#\d+(?:-\d+)?)?/g

const skillMark = Decoration.mark({ class: 'cm-ref-skill' })
const fileMark = Decoration.mark({ class: 'cm-ref-file' })

function buildDecorations(doc: Text): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>()
  const text = doc.toString()

  const matches: Array<{ start: number; end: number; deco: Decoration }> = []

  SKILL_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = SKILL_RE.exec(text)) !== null) {
    matches.push({ start: m.index, end: m.index + m[0].length, deco: skillMark })
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
 * StateField that highlights /skill and @file reference tokens in the editor.
 * Using a StateField (instead of a ViewPlugin) ensures decorations update
 * atomically with document changes, preventing cursor positioning lag.
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
