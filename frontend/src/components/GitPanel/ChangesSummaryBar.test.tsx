// @vitest-environment jsdom
//
// Unit tests for the Changes summary strip: per-kind counters (modified /
// deleted / new), the grand total, the proportional mix-bar weights, and the
// self-hiding empty state.
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ChangesSummaryBar } from './ChangesSummaryBar'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

function makeEntry(overrides: Partial<GitPanelEntry>): GitPanelEntry {
  return {
    path: '/repo/src/f.txt',
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: ' ',
    worktreeStatus: ' ',
    ...overrides,
  }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(): HTMLElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(<ChangesSummaryBar />)
  })
  return container
}

function countText(el: HTMLElement, testId: string): string {
  return el.querySelector(`[data-testid="${testId}"]`)?.textContent ?? ''
}

beforeEach(() => {
  useGitPanelStore.getState().reset()
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('ChangesSummaryBar — counters', () => {
  it('shows modified / deleted / new counts plus the grand total', () => {
    useGitPanelStore.getState().loadEntries([
      // MM — one path, one modified count (never two).
      makeEntry({ path: '/repo/mm.txt', status: 'M', staged: true, indexStatus: 'M', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/mod.txt', status: 'M', indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/del.txt', status: 'D', indexStatus: ' ', worktreeStatus: 'D' }),
      makeEntry({ path: '/repo/new.txt', status: 'A', indexStatus: '?', worktreeStatus: '?' }),
      makeEntry({ path: '/repo/added.txt', status: 'A', staged: true, indexStatus: 'A', worktreeStatus: ' ' }),
    ])

    const el = render()

    expect(countText(el, 'changes-summary-modified')).toBe('2modified')
    expect(countText(el, 'changes-summary-deleted')).toBe('1deleted')
    expect(countText(el, 'changes-summary-new')).toBe('2new')
    expect(countText(el, 'changes-summary-total')).toBe('5 total')
  })

  it('renders nothing when there are no entries', () => {
    const el = render()
    expect(el.querySelector('[data-testid="changes-summary"]')).toBeNull()
  })

  it('keeps zero-count kinds visible as 0', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/new.txt', status: 'A', indexStatus: '?', worktreeStatus: '?' }),
    ])

    const el = render()

    expect(countText(el, 'changes-summary-modified')).toBe('0modified')
    expect(countText(el, 'changes-summary-deleted')).toBe('0deleted')
    expect(countText(el, 'changes-summary-new')).toBe('1new')
    expect(countText(el, 'changes-summary-total')).toBe('1 total')
  })
})

describe('ChangesSummaryBar — proportional bar', () => {
  it('weights each segment by its kind count', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/a.txt', indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/b.txt', indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/c.txt', indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/d.txt', indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/e.txt', indexStatus: 'D', worktreeStatus: ' ' }),
      makeEntry({ path: '/repo/f.txt', status: 'A', indexStatus: '?', worktreeStatus: '?' }),
    ])

    const el = render()

    const seg = (kind: string) =>
      el.querySelector<HTMLElement>(`[data-testid="changes-summary-bar-${kind}"]`)
    expect(seg('modified')?.style.flexGrow).toBe('4')
    expect(seg('deleted')?.style.flexGrow).toBe('1')
    expect(seg('new')?.style.flexGrow).toBe('1')
  })

  it('collapses zero-count segments to zero width', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/only.txt', indexStatus: ' ', worktreeStatus: 'M' }),
    ])

    const el = render()

    const seg = (kind: string) =>
      el.querySelector<HTMLElement>(`[data-testid="changes-summary-bar-${kind}"]`)
    expect(seg('modified')?.style.flexGrow).toBe('1')
    expect(seg('deleted')?.style.flexGrow).toBe('0')
    expect(seg('new')?.style.flexGrow).toBe('0')
  })
})
