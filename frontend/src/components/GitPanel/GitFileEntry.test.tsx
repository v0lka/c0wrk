// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { GitFileEntry } from './GitFileEntry'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

const LONG_PATH =
  '/repo/src/deeply/nested/directory/structure/with-a-very-long-file-name.ts'

function makeEntry(overrides: Partial<GitPanelEntry> = {}): GitPanelEntry {
  return {
    path: LONG_PATH,
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: ' ',
    worktreeStatus: 'M',
    ...overrides,
  }
}

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return container
}

/** The truncating span that renders the file name. */
function nameSpan(container: HTMLElement): HTMLSpanElement {
  return container.querySelector('span.truncate') as HTMLSpanElement
}

/** The status badge span — the only element on the row carrying `py-px`. */
function badgeSpan(container: HTMLElement): HTMLSpanElement {
  return container.querySelector('span.py-px') as HTMLSpanElement
}

/** The staging checkbox input for this row. */
function checkbox(container: HTMLElement): HTMLInputElement {
  return container.querySelector('input[type="checkbox"]') as HTMLInputElement
}

const noop = () => {}

describe('GitFileEntry', () => {
  it('carries the workspace-relative display path as the name tooltip', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        side="worktree"
        workspaceRoot="/repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(
      'src/deeply/nested/directory/structure/with-a-very-long-file-name.ts',
    )
  })

  it('falls back to the raw path in the tooltip when no workspace root is known', () => {
    const container = render(
      <GitFileEntry entry={makeEntry()} side="worktree" onToggle={noop} onOpenDiff={noop} />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })

  it('keeps the raw path in the tooltip when it lies outside the workspace root', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        side="worktree"
        workspaceRoot="/other-repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })
})

// ─────────────────────────── Axis-aware row state ────────────────────────────

describe('GitFileEntry — staging axis', () => {
  it('renders a checked checkbox and dispatches unstage on the index axis', () => {
    const onToggle = vi.fn()
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )

    const box = checkbox(container)
    expect(box.checked).toBe(true)

    act(() => {
      box.click()
    })
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(onToggle).toHaveBeenCalledWith(LONG_PATH, 'unstage')
  })

  it('renders an unchecked checkbox and dispatches stage on the worktree axis', () => {
    const onToggle = vi.fn()
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: ' ', worktreeStatus: 'M' })}
        side="worktree"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )

    const box = checkbox(container)
    expect(box.checked).toBe(false)

    act(() => {
      box.click()
    })
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(onToggle).toHaveBeenCalledWith(LONG_PATH, 'stage')
  })

  it('gives the two rows of an MM entry opposite checkbox states and actions', () => {
    // The same file modified on both axes: two independent rows. The index
    // row is checked and unstages; the worktree row is unchecked and stages.
    const entry = makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'M' })

    const indexToggle = vi.fn()
    const indexRow = render(
      <GitFileEntry entry={entry} side="index" onToggle={indexToggle} onOpenDiff={noop} />,
    )
    expect(checkbox(indexRow).checked).toBe(true)
    act(() => {
      checkbox(indexRow).click()
    })
    expect(indexToggle).toHaveBeenCalledWith(LONG_PATH, 'unstage')

    const worktreeToggle = vi.fn()
    const worktreeRow = render(
      <GitFileEntry entry={entry} side="worktree" onToggle={worktreeToggle} onOpenDiff={noop} />,
    )
    expect(checkbox(worktreeRow).checked).toBe(false)
    act(() => {
      checkbox(worktreeRow).click()
    })
    expect(worktreeToggle).toHaveBeenCalledWith(LONG_PATH, 'stage')
  })
})

describe('GitFileEntry — status badge', () => {
  it('shows the index code on the index axis', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'D' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('M')
  })

  it('shows the worktree code on the worktree axis', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'D' })}
        side="worktree"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('D')
  })

  it('shows A for an untracked row rather than the raw ? marker', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'A', indexStatus: '?', worktreeStatus: '?' })}
        side="worktree"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('A')
  })
})

describe('GitFileEntry — merge conflict', () => {
  it('marks a conflict row with the conflict icon', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'U', indexStatus: 'U', worktreeStatus: 'U' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(container.querySelector('svg[aria-label="Merge conflict"]')).not.toBeNull()
  })

  it('does not mark a plain modification with the conflict icon', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(container.querySelector('svg[aria-label="Merge conflict"]')).toBeNull()
  })
})
