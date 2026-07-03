import { ViewPlugin, type ViewUpdate } from '@codemirror/view'
import { Decoration, EditorView } from '@codemirror/view'
import type { DecorationSet } from '@codemirror/view'
import type { Text } from '@codemirror/state'

// Conflict marker line prefixes (git default). Each starts a line.
const OURS_PREFIX = '<<<<<<<'
const SEPARATOR_PREFIX = '======='
const THEIRS_PREFIX = '>>>>>>>'

// Reused line decorations (created once — referenced by reference in the set).
const conflictOursDeco = Decoration.line({ class: 'cm-conflict-ours' })
const conflictSeparatorDeco = Decoration.line({ class: 'cm-conflict-separator' })
const conflictTheirsDeco = Decoration.line({ class: 'cm-conflict-theirs' })

/**
 * Classify a conflict marker line, or return null for a non-marker line.
 * The full-line prefix match (after trimming leading whitespace) mirrors how
 * git writes markers, and avoids false-positives on code that merely contains
 * seven-character `=`/`<`/`>` runs mid-expression.
 */
export function conflictClassForLine(text: string): 'ours' | 'separator' | 'theirs' | null {
  const trimmed = text.trimStart()
  if (trimmed.startsWith(OURS_PREFIX)) return 'ours'
  if (trimmed.startsWith(SEPARATOR_PREFIX)) return 'separator'
  if (trimmed.startsWith(THEIRS_PREFIX)) return 'theirs'
  return null
}

/**
 * Build a DecorationSet of line decorations for git conflict markers.
 * Exported as a pure function (takes a CodeMirror `Text`) so it can be
 * unit-tested in isolation against a constructed document.
 */
export function buildConflictDecorations(doc: Text): DecorationSet {
  const builder: Array<{ from: number; value: Decoration }> = []
  for (let i = 1; i <= doc.lines; i++) {
    const line = doc.line(i)
    const cls = conflictClassForLine(line.text)
    if (cls === null) continue
    const value =
      cls === 'ours'
        ? conflictOursDeco
        : cls === 'separator'
          ? conflictSeparatorDeco
          : conflictTheirsDeco
    builder.push({ from: line.from, value })
  }
  builder.sort((a, b) => a.from - b.from)
  return Decoration.set(builder.map((d) => d.value.range(d.from, d.from)))
}

/**
 * ViewPlugin that highlights git conflict markers (<<<<<<<, =======, >>>>>>>)
 * with distinct per-section line decorations. Always-on — scanning is cheap
 * and avoids needing conflict state from the Git panel.
 *
 * Follows the lib/cmDiffDecorations.ts pattern (FE-6 / D4).
 */
export const conflictMarkerPlugin = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: EditorView) {
      this.decorations = buildConflictDecorations(view.state.doc)
    }
    update(update: ViewUpdate) {
      if (update.docChanged || update.viewportChanged) {
        this.decorations = buildConflictDecorations(update.view.state.doc)
      }
    }
  },
  {
    decorations: (v) => v.decorations,
  },
)
