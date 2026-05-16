import { EditorView, ViewPlugin, Decoration, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import { RangeSetBuilder } from '@codemirror/state'

// Matches /skill-name preceded by whitespace or at line start.
const SKILL_RE = /(?:^|(?<=\s))\/([\w-]+)/g
// Matches @file-path (with escaped spaces and optional #line) preceded by whitespace or at line start.
const FILE_RE = /(?:^|(?<=\s))@(?:[^\s\\]|\\.)+(?:#\d+(?:-\d+)?)?/g

const skillMark = Decoration.mark({ class: 'cm-ref-skill' })
const fileMark = Decoration.mark({ class: 'cm-ref-file' })

function buildDecorations(view: EditorView): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>()

  for (const { from, to } of view.visibleRanges) {
    const text = view.state.sliceDoc(from, to)

    const matches: Array<{ start: number; end: number; deco: Decoration }> = []

    SKILL_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = SKILL_RE.exec(text)) !== null) {
      matches.push({ start: from + m.index, end: from + m.index + m[0].length, deco: skillMark })
    }

    FILE_RE.lastIndex = 0
    while ((m = FILE_RE.exec(text)) !== null) {
      matches.push({ start: from + m.index, end: from + m.index + m[0].length, deco: fileMark })
    }

    // DecorationSet requires sorted, non-overlapping ranges.
    matches.sort((a, b) => a.start - b.start || a.end - b.end)
    for (const { start, end, deco } of matches) {
      builder.add(start, end, deco)
    }
  }

  return builder.finish()
}

/**
 * ViewPlugin that highlights /skill and @file reference tokens in the editor.
 */
export const referenceHighlighter = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: EditorView) {
      this.decorations = buildDecorations(view)
    }
    update(update: ViewUpdate) {
      if (update.docChanged || update.viewportChanged) {
        this.decorations = buildDecorations(update.view)
      }
    }
  },
  { decorations: (v) => v.decorations },
)
