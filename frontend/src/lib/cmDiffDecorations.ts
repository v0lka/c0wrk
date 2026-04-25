import { StateField, StateEffect } from '@codemirror/state'
import { Decoration, EditorView, WidgetType } from '@codemirror/view'
import type { DecorationSet } from '@codemirror/view'
import type { Text } from '@codemirror/state'
import type { DisplayLine, CharDiffPart } from '@/lib/diffParser'

// -- State Effects -----------------------------------------------------------

export const setDiffEffect = StateEffect.define<DisplayLine[] | null>()
export const setHighlightLineEffect = StateEffect.define<number | null>()

// -- Removed Line Widget -----------------------------------------------------

class RemovedLineWidget extends WidgetType {
  constructor(readonly content: string) {
    super()
  }

  toDOM(): HTMLElement {
    const div = document.createElement('div')
    div.className = 'cm-diff-removed-widget'
    // Left padding zone matching gutter area
    const gutter = document.createElement('span')
    gutter.className = 'cm-diff-removed-gutter'
    div.appendChild(gutter)
    const text = document.createElement('span')
    text.className = 'cm-diff-removed-text'
    text.textContent = this.content
    div.appendChild(text)
    return div
  }

  eq(other: WidgetType): boolean {
    return other instanceof RemovedLineWidget && other.content === this.content
  }

  get estimatedHeight(): number {
    return 20 // 1.25rem = 20px at 16px base (14px in this app ≈ 17.5, round up)
  }
}

// -- Decoration Builders -----------------------------------------------------

const addedLineDeco = Decoration.line({ class: 'cm-diff-added' })
const modifiedLineDeco = Decoration.line({ class: 'cm-diff-modified' })
const charAddedMark = Decoration.mark({ class: 'cm-diff-char-added' })

/**
 * Convert DisplayLine[] (from diffParser) into a CodeMirror DecorationSet.
 *
 * The DisplayLine array interleaves normal, added, removed, and modified lines.
 * - "added" → line decoration on the document line
 * - "modified" → line decoration + mark decorations for charDiff
 * - "removed" → block widget before the next document line
 * - "normal" → no decoration
 */
export function convertToDecorations(displayLines: DisplayLine[], doc: Text): DecorationSet {
  const builder: Array<{ from: number; to: number; value: Decoration }> = []

  for (let i = 0; i < displayLines.length; i++) {
    const dl = displayLines[i]!
    if (dl.type === 'normal') continue

    if (dl.type === 'added' && dl.lineNumber != null) {
      const line = safeDocLine(doc, dl.lineNumber)
      if (line) {
        builder.push({ from: line.from, to: line.from, value: addedLineDeco })
      }
    } else if (dl.type === 'modified' && dl.lineNumber != null) {
      const line = safeDocLine(doc, dl.lineNumber)
      if (line) {
        builder.push({ from: line.from, to: line.from, value: modifiedLineDeco })
        // Add character-level diff marks
        if (dl.charDiff) {
          addCharDiffMarks(builder, line.from, dl.charDiff)
        }
      }
    } else if (dl.type === 'removed') {
      // Attach widget before the next document line
      const nextDocLine = findNextDocumentLine(displayLines, i)
      if (nextDocLine != null) {
        const line = safeDocLine(doc, nextDocLine)
        if (line) {
          const widget = new RemovedLineWidget(dl.content)
          builder.push({
            from: line.from,
            to: line.from,
            value: Decoration.widget({ widget, block: true, side: -1 }),
          })
        }
      } else {
        // Removed lines at the very end of the file — attach after last line
        const lastLine = doc.line(doc.lines)
        const widget = new RemovedLineWidget(dl.content)
        builder.push({
          from: lastLine.to,
          to: lastLine.to,
          value: Decoration.widget({ widget, block: true, side: 1 }),
        })
      }
    }
  }

  // Sort by from position (required by RangeSet)
  builder.sort((a, b) => a.from - b.from || a.value.startSide - b.value.startSide)

  return Decoration.set(builder.map((d) => d.value.range(d.from, d.to)))
}

function safeDocLine(doc: Text, lineNumber: number) {
  if (lineNumber < 1 || lineNumber > doc.lines) return null
  return doc.line(lineNumber)
}

/**
 * Find the next DisplayLine with a lineNumber (i.e., the next line that exists
 * in the current document) after position `idx` in the displayLines array.
 */
function findNextDocumentLine(displayLines: DisplayLine[], idx: number): number | null {
  for (let j = idx + 1; j < displayLines.length; j++) {
    if (displayLines[j]!.lineNumber != null) return displayLines[j]!.lineNumber!
  }
  return null
}

/**
 * Add Decoration.mark ranges for character-level diff parts within a line.
 */
function addCharDiffMarks(
  builder: Array<{ from: number; to: number; value: Decoration }>,
  lineFrom: number,
  charDiff: CharDiffPart[],
): void {
  let offset = lineFrom
  for (const part of charDiff) {
    if (part.type === 'added') {
      builder.push({ from: offset, to: offset + part.value.length, value: charAddedMark })
      offset += part.value.length
    } else if (part.type === 'removed') {
      // Removed chars don't exist in the current document, so we can't mark them.
      // They're already shown in the modified line's charDiff rendering by diffParser.
      // In CodeMirror, we skip removed parts since the text isn't in the document.
      // The RemovedLineWidget handles showing deleted content for full lines.
      // For inline char-level removals within a modified line, we'd need a widget —
      // but for visual parity with the current implementation, we skip them here.
      // The "modified" line shows the new content with added chars highlighted.
    } else {
      // 'equal' — advance offset without decoration
      offset += part.value.length
    }
  }
}

// -- State Fields ------------------------------------------------------------

/**
 * StateField that holds diff decorations.
 * Updated via setDiffEffect.
 */
export const diffDecorationField = StateField.define<DecorationSet>({
  create() {
    return Decoration.none
  },
  update(decos, tr) {
    for (const effect of tr.effects) {
      if (effect.is(setDiffEffect)) {
        if (effect.value == null) return Decoration.none
        return convertToDecorations(effect.value, tr.state.doc)
      }
    }
    // Map through document changes to keep positions valid
    if (tr.docChanged) return decos.map(tr.changes)
    return decos
  },
  provide(field) {
    return EditorView.decorations.from(field)
  },
})

/**
 * StateField that holds the highlighted line decoration (for scroll-to-line).
 * Updated via setHighlightLineEffect.
 */
export const highlightLineField = StateField.define<DecorationSet>({
  create() {
    return Decoration.none
  },
  update(decos, tr) {
    for (const effect of tr.effects) {
      if (effect.is(setHighlightLineEffect)) {
        const lineNo = effect.value
        if (lineNo == null || lineNo < 1 || lineNo > tr.state.doc.lines) {
          return Decoration.none
        }
        const line = tr.state.doc.line(lineNo)
        return Decoration.set([
          Decoration.line({ class: 'cm-highlighted-line' }).range(line.from),
        ])
      }
    }
    if (tr.docChanged) return decos.map(tr.changes)
    return decos
  },
  provide(field) {
    return EditorView.decorations.from(field)
  },
})
