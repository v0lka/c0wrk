// The chat input's /-ref highlighting must use the SAME kind→color mapping as
// the autocomplete list and the chat chips: /agent → yellow (cm-ref-agent),
// /mcp → blue (cm-ref-mcp), /skill → green (cm-ref-skill), a plain /name
// defaulting to the skill tint, and @file → info (cm-ref-file). These are the
// classes index.css colors under `.cm-chat-container`.
// @vitest-environment node
import { describe, it, expect } from 'vitest'
import { EditorState } from '@codemirror/state'
import { referenceHighlighter } from './cmReferenceHighlighter'

/** Every marked range in the doc as (text, class). */
function marksFor(doc: string): Array<{ text: string; cls: string }> {
  const state = EditorState.create({ doc, extensions: [referenceHighlighter] })
  const set = state.field(referenceHighlighter)
  const out: Array<{ text: string; cls: string }> = []
  set.between(0, state.doc.length, (from, to, value) => {
    out.push({ text: state.sliceDoc(from, to), cls: (value.spec as { class?: string }).class ?? '' })
  })
  return out
}

describe('cmReferenceHighlighter — kind-colored /-refs', () => {
  it('colors qualified refs by kind and a plain ref as a skill', () => {
    expect(marksFor('/agent: reviewer /mcp: context7 /skill: commit /plain')).toEqual([
      { text: '/agent: reviewer', cls: 'cm-ref-agent' },
      { text: '/mcp: context7', cls: 'cm-ref-mcp' },
      { text: '/skill: commit', cls: 'cm-ref-skill' },
      { text: '/plain', cls: 'cm-ref-skill' },
    ])
  })

  it('keeps the @file mark and the legacy #agent mark', () => {
    const marks = marksFor('see @x.go#L20 and #legacy')
    expect(marks).toContainEqual({ text: '@x.go#L20', cls: 'cm-ref-file' })
    expect(marks).toContainEqual({ text: '#legacy', cls: 'cm-ref-agent' })
  })

  it('does not color non-boundary or path-like slash tokens', () => {
    expect(marksFor('/github/cache/config.json')).toEqual([])
    expect(marksFor('a/b')).toEqual([])
  })
})
