// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { GitGraphCard } from './GitGraphCard'
import {
  cardHeight,
  expandedCardHeight,
  STATUS_LINE_HEIGHT,
  type CardExpansionState,
} from './gitGraphCardHeights'
import type { GraphNode } from '@/lib/gitGraphLayout'
import type { GitHistoryCommit } from '@/types/models'

let container: HTMLDivElement
let root: Root

const onToggle = vi.fn()
const onToggleFile = vi.fn()

function node(overrides: Partial<GraphNode>): GraphNode {
  return {
    sha: 'aaa1111',
    lane: 0,
    row: 0,
    message: 'feat: add thing',
    refs: [],
    parents: [{ sha: 'bbb2222', lane: 0 }],
    isMerge: false,
    ...overrides,
  }
}

function commit(overrides: Partial<GitHistoryCommit>): GitHistoryCommit {
  return {
    sha: 'aaa1111',
    parents: ['bbb2222'],
    author: 'Jane',
    email: 'j@x',
    date: '2026-07-10T10:00:00Z',
    message: 'feat: add thing',
    refs: [],
    ...overrides,
  }
}

interface RenderOpts {
  node?: GraphNode
  commit?: GitHistoryCommit
  state: CardExpansionState
  expanded?: boolean
  diffViewMode?: 'unified' | 'split'
}

function render(opts: RenderOpts) {
  const { node: n = node({}), commit: c = commit({}), state, expanded = false, diffViewMode = 'unified' } = opts
  act(() => {
    root.render(
      <GitGraphCard
        node={n}
        commit={c}
        height={cardHeight(state, expanded)}
        expanded={expanded}
        messageLoading={state.messageLoading}
        messageFailed={state.messageFailed}
        fullMessage={state.fullMessage}
        files={state.files}
        filesLoading={state.filesLoading}
        fileDiffs={state.fileDiffs}
        diffViewMode={diffViewMode}
        onToggle={onToggle}
        onToggleFile={onToggleFile}
      />,
    )
  }
  )
}

beforeEach(() => {
  onToggle.mockReset()
  onToggleFile.mockReset()
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

describe('GitGraphCard — collapsed card', () => {
  it('renders the two-line header (message, sha, author · time) and no expansion', () => {
    render({ state: base() })
    expect(container.textContent).toContain('feat: add thing')
    expect(container.textContent).toContain('aaa1111')
    expect(container.textContent).toContain('Jane')
    // No expansion content.
    expect(container.textContent).not.toContain('Loading files…')
    expect(container.textContent).not.toContain('Full message body')
  })

  it('renders ref decorations and the backend stats triple on the header', () => {
    render({
      node: node({ refs: ['HEAD -> main'] }),
      commit: commit({ stats: { added: 2, modified: 1, deleted: 3 } }),
      state: base(),
    })
    expect(container.textContent).toContain('main')
    expect(container.textContent).toContain('+2')
    expect(container.textContent).toContain('~1')
    expect(container.textContent).toContain('−3')
  })

  it('derives the stats triple from the fetched files when the backend sent none', () => {
    render({
      state: base({
        files: [
          { path: 'a', status: 'A' },
          { path: 'b', status: 'M' },
        ],
      }),
      expanded: true,
    })
    // Same bucketing as the backend: A→added, M→modified (deleted renders as −0).
    expect(container.textContent).toContain('+1')
    expect(container.textContent).toContain('~1')
    expect(container.textContent).toContain('−0')
  })

  it('hides the stats badges when there is nothing to count', () => {
    render({ state: base() })
    expect(container.textContent).not.toContain('+0')
  })

  it('clicking the header fires onToggle', () => {
    render({ state: base() })
    const header = Array.from(container.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes('feat: add thing'),
    ) as HTMLButtonElement
    header.click()
    expect(onToggle).toHaveBeenCalledTimes(1)
  })
})

describe('GitGraphCard — expanded card', () => {
  it('renders the full message in a capped scrollable box sized by the height math', () => {
    render({ state: base({ fullMessage: 'subject\n\nbody' }), expanded: true })
    expect(container.textContent).toContain('subject')
    expect(container.textContent).toContain('body')
    const msgBox = container.querySelector('.overflow-y-auto') as HTMLElement
    expect(msgBox).toBeTruthy()
    const expected = expandedCardHeight(base({ fullMessage: 'subject\n\nbody' }))
    expect(msgBox.style.height).toBe(`${8 + 3 * 16}px`)
    // The card's total height is the header plus exactly the computed expansion.
    expect(expected).toBeGreaterThan(0)
  })

  it('shows a loading placeholder for the message while it is fetched', () => {
    render({ state: base({ messageLoading: true }), expanded: true })
    expect(container.textContent).toContain('Loading message…')
    const ph = Array.from(container.querySelectorAll('div')).find((el) => el.style.height === `${STATUS_LINE_HEIGHT}px`)
    expect(ph).toBeTruthy()
  })

  it('shows an error placeholder when the message fetch failed', () => {
    render({ state: base({ messageFailed: true }), expanded: true })
    expect(container.textContent).toContain('Failed to load message')
  })

  it('shows a loading line while files are fetched', () => {
    render({ state: base({ filesLoading: true }), expanded: true })
    expect(container.textContent).toContain('Loading files…')
  })

  it('lists files with status letter + path and fires onToggleFile on click', () => {
    const state = base({
      files: [
        { path: 'src/a.go', status: 'A' },
        { path: 'README.md', status: 'M' },
      ],
    })
    render({ state, expanded: true })
    expect(container.textContent).toContain('src/a.go')
    expect(container.textContent).toContain('README.md')
    const fileRow = Array.from(container.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes('src/a.go'),
    ) as HTMLButtonElement
    fileRow.click()
    expect(onToggleFile).toHaveBeenCalledWith('src/a.go')
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('renders an expanded file diff body from its state slice', () => {
    const state = base({
      files: [{ path: 'src/a.go', status: 'A' }],
      fileDiffs: {
        'src/a.go': {
          expanded: true,
          loading: false,
          failed: false,
          diffViewMode: 'unified',
          slice: { path: 'src/a.go', hunks: [{ raw: '+hello', old_start: 1, new_start: 1 }] },
        },
      },
    })
    render({ state, expanded: true })
    expect(container.textContent).toContain('hello')
    // One diff table row for the single raw line.
    expect(container.querySelectorAll('tbody tr').length).toBe(1)
  })

  it('shows the diff loading placeholder for an expanded file without a slice yet', () => {
    const state = base({
      files: [{ path: 'src/a.go', status: 'A' }],
      fileDiffs: {
        'src/a.go': { expanded: true, loading: true, failed: false, slice: undefined, diffViewMode: 'unified' },
      },
    })
    render({ state, expanded: true })
    expect(container.textContent).toContain('Loading diff…')
  })
})

describe('GitGraphCard — empty states', () => {
  it('merge commit with no files: "No diff (merge commit)"', () => {
    render({
      node: node({
        isMerge: true,
        parents: [
          { sha: 'p1', lane: 0 },
          { sha: 'p2', lane: 1 },
        ],
      }),
      commit: commit({ parents: ['p1', 'p2'] }),
      state: base({ files: [] }),
      expanded: true,
    })
    expect(container.textContent).toContain('No diff (merge commit)')
    expect(container.textContent).not.toContain('No files')
  })

  it('root commit with no files: "No files (root commit)"', () => {
    render({
      node: node({ isMerge: false, parents: [] }),
      commit: commit({ parents: [] }),
      state: base({ files: [] }),
      expanded: true,
    })
    expect(container.textContent).toContain('No files (root commit)')
    expect(container.textContent).not.toContain('No diff (merge commit)')
  })

  it('ordinary commit with no files: "No files"', () => {
    render({ state: base({ files: [] }), expanded: true })
    expect(container.textContent).toContain('No files')
    expect(container.textContent).not.toContain('(root commit)')
    expect(container.textContent).not.toContain('(merge commit)')
  })
})

/** Minimal collapsed-card state. */
function base(overrides: Partial<CardExpansionState> = {}): CardExpansionState {
  return {
    messageLoading: false,
    messageFailed: false,
    fullMessage: undefined,
    filesLoading: false,
    files: undefined,
    fileDiffs: {},
    ...overrides,
  }
}
