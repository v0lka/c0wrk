// @vitest-environment jsdom
//
// Integration test for the Changes list's two-axis section routing. The list
// splits every entry along its porcelain axis (see `classifyEntries`): an `MM`
// file is modified on BOTH the index and the worktree, so its one path must
// render as two independent rows — one under "Staged Changes" (checked,
// unstages) and one under "Changes" (unchecked, stages).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ChangesList } from './ChangesList'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// Radix dropdown positioning (Sort/Group controls) observes its trigger with
// ResizeObserver, which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

const MM_PATH = '/repo/src/mm.txt'

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

const noop = () => Promise.resolve(true)

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(): HTMLElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(<ChangesList onToggleFile={noop} onOpenDiff={noop} />)
  })
  return container
}

/** The section header button whose title span matches `title`. */
function headerButton(el: HTMLElement, title: string): HTMLButtonElement | null {
  return (
    Array.from(el.querySelectorAll('button')).find((b) =>
      Array.from(b.querySelectorAll('span')).some((s) => s.textContent === title),
    ) ?? null
  )
}

/**
 * The section's root element — the header button's parent, which also holds
 * the rendered rows. `null` when the section rendered nothing (zero entries).
 */
function sectionRoot(el: HTMLElement, title: string): HTMLElement | null {
  return headerButton(el, title)?.parentElement ?? null
}

/** The count badge shown on a section header. */
function headerCount(el: HTMLElement, title: string): number {
  const btn = headerButton(el, title)
  if (!btn) return -1
  const spans = Array.from(btn.querySelectorAll('span'))
  return Number(spans[spans.length - 1]?.textContent ?? NaN)
}

/** File-name spans (the truncating span) whose display title equals `path`. */
function nameSpans(el: HTMLElement, path: string): HTMLSpanElement[] {
  return Array.from(el.querySelectorAll('span.truncate')).filter(
    (s) => s.getAttribute('title') === path,
  ) as HTMLSpanElement[]
}

function rowCheckbox(el: HTMLElement): HTMLInputElement {
  return el.querySelector('input[type="checkbox"]') as HTMLInputElement
}

beforeEach(() => {
  useGitPanelStore.getState().reset()
  useGitPanelStore.setState({ viewMode: 'flat', groupBy: 'none' })
  useProjectStore.setState({ projects: null, activeProjectId: 'p1' })

  container = null
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
  document.body.innerHTML = ''
})

describe('ChangesList — two-axis section routing', () => {
  it('renders an MM entry under BOTH Staged Changes and Changes', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: MM_PATH, status: 'M', staged: true, indexStatus: 'M', worktreeStatus: 'M' }),
    ])

    const el = render()

    // One path → exactly two rows (index + worktree), never more.
    expect(nameSpans(el, MM_PATH)).toHaveLength(2)

    const staged = sectionRoot(el, 'Staged Changes')
    const changes = sectionRoot(el, 'Changes')
    expect(staged).not.toBeNull()
    expect(changes).not.toBeNull()

    // The path appears once under each section, each showing a single row.
    expect(nameSpans(staged!, MM_PATH)).toHaveLength(1)
    expect(nameSpans(changes!, MM_PATH)).toHaveLength(1)
    expect(headerCount(el, 'Staged Changes')).toBe(1)
    expect(headerCount(el, 'Changes')).toBe(1)

    // No untracked axis: that section renders nothing.
    expect(sectionRoot(el, 'Untracked Files')).toBeNull()
  })

  it('marks the index row checked/unstage and the worktree row unchecked/stage', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: MM_PATH, status: 'M', staged: true, indexStatus: 'M', worktreeStatus: 'M' }),
    ])

    const el = render()
    const stagedRow = sectionRoot(el, 'Staged Changes')!
    const changesRow = sectionRoot(el, 'Changes')!

    const indexBox = rowCheckbox(stagedRow)
    expect(indexBox.checked).toBe(true)

    const worktreeBox = rowCheckbox(changesRow)
    expect(worktreeBox.checked).toBe(false)

    const onToggle = vi.fn()
    // Re-render with a spying callback to capture the dispatched batch action.
    act(() => {
      root!.render(<ChangesList onToggleFile={onToggle} onOpenDiff={noop} />)
    })

    const stagedAgain = sectionRoot(el, 'Staged Changes')!
    const changesAgain = sectionRoot(el, 'Changes')!

    act(() => {
      rowCheckbox(stagedAgain).click()
    })
    expect(onToggle).toHaveBeenCalledWith(MM_PATH, 'unstage')

    act(() => {
      rowCheckbox(changesAgain).click()
    })
    expect(onToggle).toHaveBeenCalledWith(MM_PATH, 'stage')
  })

  it('routes each entry to the section matching its porcelain axis', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/staged-only.txt', status: 'M', staged: true, indexStatus: 'M', worktreeStatus: ' ' }),
      makeEntry({ path: '/repo/worktree-only.txt', status: 'M', staged: false, indexStatus: ' ', worktreeStatus: 'M' }),
      makeEntry({ path: '/repo/untracked.txt', status: 'A', staged: false, indexStatus: '?', worktreeStatus: '?' }),
    ])

    const el = render()

    const staged = sectionRoot(el, 'Staged Changes')!
    const changes = sectionRoot(el, 'Changes')!

    expect(nameSpans(staged, '/repo/staged-only.txt')).toHaveLength(1)
    expect(nameSpans(staged, '/repo/worktree-only.txt')).toHaveLength(0)
    expect(nameSpans(staged, '/repo/untracked.txt')).toHaveLength(0)

    expect(nameSpans(changes, '/repo/worktree-only.txt')).toHaveLength(1)
    expect(nameSpans(changes, '/repo/staged-only.txt')).toHaveLength(0)
    expect(nameSpans(changes, '/repo/untracked.txt')).toHaveLength(0)

    // "Untracked Files" is collapsed by default: its header exists (count 1)
    // but the row is hidden until expanded.
    expect(headerCount(el, 'Untracked Files')).toBe(1)
    const untracked = sectionRoot(el, 'Untracked Files')!
    expect(nameSpans(untracked, '/repo/untracked.txt')).toHaveLength(0)

    act(() => {
      headerButton(el, 'Untracked Files')!.click()
    })
    expect(nameSpans(sectionRoot(el, 'Untracked Files')!, '/repo/untracked.txt')).toHaveLength(1)
  })
})

// ─────────────────────────── Tree mode file naming ───────────────────────────

describe('ChangesList — tree mode file naming', () => {
  it('renders only the basename in a tree file row, keeping the full path in the tooltip', () => {
    const deep = '/repo/src/deep/nested/f.txt'
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: deep, status: 'M', staged: false, indexStatus: ' ', worktreeStatus: 'M' }),
    ])
    useGitPanelStore.setState({
      viewMode: 'tree',
      // Display-relative paths — the same semantics buildTree/expandedDirs use.
      // Every ancestor directory must be expanded for the leaf to render.
      expandedDirs: new Set(['src', 'src/deep', 'src/deep/nested']),
    })
    useProjectStore.setState({
      projects: [{
        id: 'p1',
        name: 'repo',
        workspace_path: '/repo',
        is_external: false,
        is_no_project: false,
        created_at: '',
        last_active_at: '',
      }],
      activeProjectId: 'p1',
    })

    const el = render()

    // The row locates by its workspace-relative tooltip; the visible text is
    // the bare basename — the directory structure is carried by the tree.
    const rows = nameSpans(el, 'src/deep/nested/f.txt')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.textContent).toBe('f.txt')
  })

  it('tree dir rows are keyboard-reachable: Enter/Space collapse and re-expand', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/src/f.txt', status: 'M', staged: false, indexStatus: ' ', worktreeStatus: 'M' }),
    ])
    useGitPanelStore.setState({ viewMode: 'tree', expandedDirs: new Set(['src']) })
    useProjectStore.setState({
      projects: [{
        id: 'p1',
        name: 'repo',
        workspace_path: '/repo',
        is_external: false,
        is_no_project: false,
        created_at: '',
        last_active_at: '',
      }],
      activeProjectId: 'p1',
    })

    const el = render()

    // The dir row is the only div[role=button] in the list — section headers
    // are native buttons and file rows are checkbox rows without the role.
    const dirs = el.querySelectorAll('div[role="button"][aria-expanded]')
    expect(dirs).toHaveLength(1)
    const dir = dirs[0] as HTMLElement
    expect(dir.textContent).toBe('src')
    expect(dir.getAttribute('tabindex')).toBe('0')

    // Enter collapses; the child file row unmounts with the subtree.
    act(() => {
      dir.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    expect(dir.getAttribute('aria-expanded')).toBe('false')
    expect(nameSpans(el, 'src/f.txt')).toHaveLength(0)

    // Space re-expands.
    act(() => {
      dir.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
    })
    expect(dir.getAttribute('aria-expanded')).toBe('true')
    expect(nameSpans(el, 'src/f.txt')).toHaveLength(1)
  })
})

// ─────────────────────── Worktree focus display root ─────────────────────────

describe('ChangesList — worktree focus display root', () => {
  const WORKTREE = '/repo/.worktrees/s-abc12345'
  const WORKTREE_FILE = `${WORKTREE}/src/f.txt`

  function makeFocus() {
    return {
      path: WORKTREE,
      name: 's-abc12345',
      kind: 'managed' as const,
      branch: 'sess/s-abc12345',
      managed: true,
      pinned: true,
    }
  }

  it('displays paths relative to the focused worktree and passes absolute paths to callbacks', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: WORKTREE_FILE, status: 'M', staged: false, indexStatus: ' ', worktreeStatus: 'M' }),
    ])
    // The backend keys the status onto the focus worktree root; the display
    // root is that same root (NOT the project workspace path, which would
    // leave a `.worktrees/s-abc12345/` prefix on every row).
    useGitPanelStore.setState({ focus: makeFocus() })
    useProjectStore.setState({
      projects: [{
        id: 'p1',
        name: 'repo',
        workspace_path: '/repo',
        is_external: false,
        is_no_project: false,
        created_at: '',
        last_active_at: '',
      }],
      activeProjectId: 'p1',
    })

    const el = render()
    const onToggle = vi.fn()
    // Re-render with a spying toggle callback (mirrors the two-axis test above).
    act(() => {
      root!.render(<ChangesList onToggleFile={onToggle} onOpenDiff={noop} />)
    })

    // The row renders with the worktree-relative display path…
    const rows = nameSpans(el, 'src/f.txt')
    expect(rows).toHaveLength(1)
    // …and never with the checkout-relative (`.worktrees/…`) or absolute path.
    expect(nameSpans(el, '.worktrees/s-abc12345/src/f.txt')).toHaveLength(0)
    expect(nameSpans(el, WORKTREE_FILE)).toHaveLength(0)

    // The staging checkbox still dispatches the ABSOLUTE entry path.
    act(() => {
      rowCheckbox(sectionRoot(el, 'Changes')!).click()
    })
    expect(onToggle).toHaveBeenCalledWith(WORKTREE_FILE, 'stage')
  })

  it('falls back to the project workspace path when no focus is resolved', () => {
    useGitPanelStore.getState().loadEntries([
      makeEntry({ path: '/repo/src/f.txt', status: 'M', staged: false, indexStatus: ' ', worktreeStatus: 'M' }),
    ])
    // focus stays null (pre-resolve); the workspace path stands in.
    useProjectStore.setState({
      projects: [{
        id: 'p1',
        name: 'repo',
        workspace_path: '/repo',
        is_external: false,
        is_no_project: false,
        created_at: '',
        last_active_at: '',
      }],
      activeProjectId: 'p1',
    })

    const el = render()

    expect(nameSpans(el, 'src/f.txt')).toHaveLength(1)
    expect(nameSpans(el, '/repo/src/f.txt')).toHaveLength(0)
  })
})
