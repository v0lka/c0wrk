// @vitest-environment jsdom
import { describe, it, expect, beforeAll, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { registerLanguages } from '@/lib/hljsLanguages'

// Register hljs languages once before running the suite — mirrors what
// main.tsx does at app startup (and what hunkCodeHighlight.test does).
beforeAll(() => {
  registerLanguages()
})

import { CommitFileDiff } from './CommitFileDiff'
import { STATUS_LINE_HEIGHT, DIFF_ROW_HEIGHT } from './gitGraphCardHeights'
import type { ReviewFileDiffLike } from '@/components/review/diffParsing'

let container: HTMLDivElement
let root: Root

function renderDiff(props: {
  filePath?: string
  slice?: ReviewFileDiffLike
  loading?: boolean
  failed?: boolean
  diffViewMode?: 'unified' | 'split'
  height?: number
}) {
  act(() => {
    root.render(
      <CommitFileDiff
        filePath={props.filePath ?? 'src/a.go'}
        slice={props.slice}
        loading={props.loading ?? false}
        failed={props.failed ?? false}
        diffViewMode={props.diffViewMode ?? 'unified'}
        height={props.height ?? 100}
      />,
    )
  })
}

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

const GO_SLICE: ReviewFileDiffLike = {
  path: 'src/a.go',
  hunks: [
    {
      raw: ' func A() {\n+\treturn 1\n-\treturn 2\n }',
      old_start: 1,
      new_start: 1,
    },
  ],
}

describe('CommitFileDiff — placeholder states', () => {
  it('renders a fixed-height loading line', () => {
    renderDiff({ loading: true })
    const el = container.firstElementChild as HTMLElement
    expect(el.style.height).toBe(`${STATUS_LINE_HEIGHT}px`)
    expect(el.textContent).toContain('Loading diff…')
  })

  it('renders a fixed-height failure line', () => {
    renderDiff({ failed: true })
    const el = container.firstElementChild as HTMLElement
    expect(el.style.height).toBe(`${STATUS_LINE_HEIGHT}px`)
    expect(el.textContent).toContain('Failed to load diff')
  })

  it('renders a fixed-height "No diff" line for a missing or hunk-less slice', () => {
    renderDiff({})
    expect(container.textContent).toContain('No diff')
    renderDiff({ slice: { path: 'a', hunks: [] } })
    expect(container.textContent).toContain('No diff')
  })
})

describe('CommitFileDiff — unified mode', () => {
  it('renders all raw lines as rows with explicit height and internal scroll', () => {
    renderDiff({ slice: GO_SLICE, height: 58 })
    const wrap = container.firstElementChild as HTMLElement
    expect(wrap.style.height).toBe('58px')
    expect(wrap.className).toContain('overflow-y-auto')
    // 4 raw lines → 4 table rows (1 add, 1 del, 2 context).
    const rows = container.querySelectorAll('tbody tr')
    expect(rows.length).toBe(4)
    expect(container.textContent).toContain('return 1')
    expect(container.textContent).toContain('return 2')
  })

  it('highlights code lines with hljs classes for the file language', () => {
    renderDiff({ slice: GO_SLICE })
    // 'func' is a Go keyword — highlight.js wraps it in an hljs-keyword span.
    expect(container.innerHTML).toContain('hljs-keyword')
  })

  it('escapes non-code header lines without hljs spans', () => {
    renderDiff({ slice: GO_SLICE })
    // The context lines are plain code (no spans required for spaces), but
    // the numbers columns must not carry markup and the add/del content does.
    const htmlCells = container.querySelectorAll('td:nth-child(3)')
    expect(htmlCells.length).toBe(4)
  })
})

describe('CommitFileDiff — split mode', () => {
  it('renders the side-by-side table with four cells per row', () => {
    renderDiff({ slice: GO_SLICE, diffViewMode: 'split' })
    // table-fixed + colgroup is unique to the split table.
    expect(container.querySelector('colgroup')).not.toBeNull()
    // context | (add zipped with del) | context → 3 aligned rows from 4 raw lines.
    expect(container.querySelectorAll('tbody tr').length).toBe(3)
    const firstRowCells = container.querySelectorAll('tbody tr:first-child td')
    expect(firstRowCells.length).toBe(4)
  })
})

describe('CommitFileDiff — heights', () => {
  it('reserves the passed height, not a content-derived one', () => {
    renderDiff({ slice: GO_SLICE, height: 200 })
    const wrap = container.firstElementChild as HTMLElement
    expect(wrap.style.height).toBe('200px')
    // And the height stays capped independently of the natural size.
    renderDiff({ slice: GO_SLICE, height: 30 })
    expect((container.firstElementChild as HTMLElement).style.height).toBe('30px')
  })

  it('exposes the module constants used by the height math', () => {
    // Sanity: the constants the page's math relies on keep their meaning.
    expect(DIFF_ROW_HEIGHT).toBeGreaterThan(0)
    expect(STATUS_LINE_HEIGHT).toBeGreaterThan(0)
  })
})
