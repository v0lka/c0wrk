// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// GitPanel's own tab routing is under test: everything it renders per tab
// (and the side-effect hook) is mocked with sentinels so assertions stay
// about WHICH section is mounted for WHICH internal tab.
vi.mock('@/hooks/useGitStatusEvents', () => ({ useGitStatusEvents: vi.fn() }))
vi.mock('@/hooks/useGitFocusSync', () => ({ useGitFocusSync: vi.fn() }))
vi.mock('./GitPanelToolbar', () => ({ GitPanelToolbar: () => createElement('div', { 'data-testid': 'git-toolbar' }) }))
vi.mock('./ChangesList', () => ({
  ChangesList: () => createElement('div', { 'data-testid': 'changes-list' }, 'CHANGES'),
}))
vi.mock('./CommitSection', () => ({ CommitSection: () => createElement('div', { 'data-testid': 'commit-section' }) }))
vi.mock('./GitHistoryTab', () => ({
  GitHistoryTab: () => createElement('div', { 'data-testid': 'history-tab' }, 'HISTORY'),
}))
vi.mock('./GitPanelFooter', () => ({ GitPanelFooter: () => createElement('div', { 'data-testid': 'git-footer' }) }))
vi.mock('./BranchPicker', () => ({ BranchPicker: () => null }))
vi.mock('@/components/layout/FileTreePanel', () => ({
  FileTreePanel: () => createElement('div', { 'data-testid': 'file-tree' }, 'FILE_TREE'),
}))

import { GitPanel } from './index'
import { useGitPanelStore, selectGitPanelTab } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'

let root: Root | null = null
let container: HTMLDivElement | null = null

function renderPanel(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(GitPanel))
  })
}

function has(testId: string): boolean {
  return container?.querySelector(`[data-testid="${testId}"]`) != null
}

beforeEach(() => {
  useProjectStore.setState({ activeProjectId: 'p1' })
  useGitPanelStore.getState().reset()
  act(() => {
    useGitPanelStore.getState().setGitRepo(true, 'p1')
  })
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

describe('GitPanel — internal tab routing', () => {
  it('renders files | changes | history with files first and active by default', () => {
    renderPanel()

    const labels = Array.from(container!.querySelectorAll('button[type="button"]'))
      .map((b) => b.textContent?.trim().toLowerCase())
      .filter((t): t is string => t === 'files' || t === 'changes' || t === 'history')
    expect(labels).toEqual(['files', 'changes', 'history'])

    // The default (reset) activeTab is 'files': the explorer is mounted.
    expect(has('file-tree')).toBe(true)
    expect(has('changes-list')).toBe(false)
    expect(has('history-tab')).toBe(false)
  })

  it('switching to changes mounts the changes section and unmounts the explorer', () => {
    renderPanel()

    const changesBtn = Array.from(container!.querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .find((b) => b.textContent?.trim().toLowerCase() === 'changes')!
    act(() => {
      changesBtn.click()
    })

    expect(selectGitPanelTab(useGitPanelStore.getState(), 'p1')).toBe('changes')
    expect(has('file-tree')).toBe(false)
    expect(has('changes-list')).toBe(true)
    expect(has('commit-section')).toBe(true)
  })

  it('switching to history mounts the history tab', () => {
    renderPanel()

    const historyBtn = Array.from(container!.querySelectorAll<HTMLButtonElement>('button[type="button"]'))
      .find((b) => b.textContent?.trim().toLowerCase() === 'history')!
    act(() => {
      historyBtn.click()
    })

    expect(selectGitPanelTab(useGitPanelStore.getState(), 'p1')).toBe('history')
    expect(has('file-tree')).toBe(false)
    expect(has('history-tab')).toBe(true)
  })

  it('restores each project its own section on switch-back (per-project tab)', () => {
    useGitPanelStore.getState().setActiveTab('p1', 'changes')
    useGitPanelStore.getState().setActiveTab('p2', 'history')

    renderPanel()
    expect(has('changes-list')).toBe(true)
    expect(has('file-tree')).toBe(false)

    act(() => {
      useProjectStore.setState({ activeProjectId: 'p2' })
      // A production project switch re-checks git-repo status for the new
      // project (useProjectGitRepo) — mirror the completed check so the
      // panel stays mounted under the paired isGitRepo contract.
      useGitPanelStore.getState().setGitRepo(true, 'p2')
    })

    expect(has('history-tab')).toBe(true)
    expect(has('changes-list')).toBe(false)
  })
})

describe('GitPanel — git-repo pairing', () => {
  it('renders the panel when isGitRepo was checked for the active project', () => {
    renderPanel()

    expect(container!.textContent).not.toContain('Not a git repository')
    expect(has('file-tree')).toBe(true)
  })

  it('shows "Not a git repository" for a stale isGitRepo from a previously active project', () => {
    // isGitRepo=true was checked for p2 while p1 is active: the paired
    // selector must not leak the stale result (store contract —
    // isGitRepo && gitRepoProjectId === activeProjectId).
    act(() => {
      useGitPanelStore.getState().setGitRepo(true, 'p2')
    })
    renderPanel()

    expect(container!.textContent).toContain('Not a git repository')
    expect(has('file-tree')).toBe(false)
  })
})
