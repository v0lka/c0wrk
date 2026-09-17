// paperSourceBlocks — the Extracted sub-view's block segmentation. The ranges
// must tile the document's non-blank lines exactly, so any line an anchor
// resolves to (lib/paperAnchors works on line indexes) maps to one block.

import { describe, expect, it } from 'vitest'
import { blockContainingLine, splitSourceBlocks } from './paperSourceBlocks'

describe('splitSourceBlocks', () => {
  it('splits on blank lines and records exact line ranges', () => {
    const source = '# Title\n\n## 3 Method\n\nBody line.\n\nMore body.\n'
    // Lines: 0 '# Title', 1 '', 2 '## 3 Method', 3 '', 4 'Body line.',
    // 5 '', 6 'More body.', 7 '' (trailing newline).
    expect(splitSourceBlocks(source)).toEqual([
      { text: '# Title', startLine: 0, endLine: 0 },
      { text: '## 3 Method', startLine: 2, endLine: 2 },
      { text: 'Body line.', startLine: 4, endLine: 4 },
      { text: 'More body.', startLine: 6, endLine: 6 },
    ])
  })

  it('keeps multi-line paragraphs as one block', () => {
    const source = 'line one\nline two\nline three\n\nnext block'
    expect(splitSourceBlocks(source)).toEqual([
      { text: 'line one\nline two\nline three', startLine: 0, endLine: 2 },
      { text: 'next block', startLine: 4, endLine: 4 },
    ])
  })

  it('collapses runs of blank lines without emitting empty blocks', () => {
    const source = 'a\n\n\n\n\nb'
    expect(splitSourceBlocks(source)).toEqual([
      { text: 'a', startLine: 0, endLine: 0 },
      { text: 'b', startLine: 5, endLine: 5 },
    ])
  })

  it('keeps blank lines inside a fenced code block in the fence block', () => {
    const source = 'para\n\n```python\ndef f():\n\n    return 1\n```\n\nafter'
    // Lines: 0 para, 2-6 the fence (blank line 4 inside), 8 after.
    expect(splitSourceBlocks(source)).toEqual([
      { text: 'para', startLine: 0, endLine: 0 },
      { text: '```python\ndef f():\n\n    return 1\n```', startLine: 2, endLine: 6 },
      { text: 'after', startLine: 8, endLine: 8 },
    ])
  })

  it('treats an unclosed fence as running to the end of the document', () => {
    const source = '```\ncode\n\nstill code'
    expect(splitSourceBlocks(source)).toEqual([
      { text: '```\ncode\n\nstill code', startLine: 0, endLine: 3 },
    ])
  })

  it('accepts tilde fences and indented (≤3 spaces) fences', () => {
    const source = '~~~\nblank\n\ninside\n~~~\n\n  ```js\nx\n\ny\n  ```'
    expect(splitSourceBlocks(source)).toEqual([
      { text: '~~~\nblank\n\ninside\n~~~', startLine: 0, endLine: 4 },
      { text: '  ```js\nx\n\ny\n  ```', startLine: 6, endLine: 10 },
    ])
  })

  it('returns [] for an empty document', () => {
    expect(splitSourceBlocks('')).toEqual([])
  })

  it('returns [] for a document of only blank lines', () => {
    expect(splitSourceBlocks('\n\n  \n')).toEqual([])
  })
})

describe('blockContainingLine', () => {
  const blocks = splitSourceBlocks('# T\n\none\ntwo\n\n``` \na\n\nb\n```\n')

  it('maps a hit on a block start, middle, and end line to that block', () => {
    expect(blockContainingLine(blocks, 0)?.startLine).toBe(0)
    expect(blockContainingLine(blocks, 2)?.startLine).toBe(2)
    expect(blockContainingLine(blocks, 3)?.startLine).toBe(2)
    // Line 6 is blank INSIDE the fence — it belongs to the fence block.
    expect(blockContainingLine(blocks, 6)?.startLine).toBe(5)
    expect(blockContainingLine(blocks, 8)?.startLine).toBe(5)
  })

  it('returns null for separator lines and out-of-range lines', () => {
    expect(blockContainingLine(blocks, 1)).toBeNull()
    expect(blockContainingLine(blocks, 4)).toBeNull()
    expect(blockContainingLine(blocks, -1)).toBeNull()
    expect(blockContainingLine(blocks, 99)).toBeNull()
    expect(blockContainingLine([], 0)).toBeNull()
  })
})
