// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { WorktreesSection } from './WorktreesSection'
import type { GitWorktree } from '@/types/models'

let container: HTMLDivElement | null = null
let root: Root | null = null

function renderSection(
  worktrees: GitWorktree[],
  onSelect: (w: GitWorktree) => void = vi.fn(),
  busyPath: string | null = null,
): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(
      <WorktreesSection worktrees={worktrees} onSelect={onSelect} busyPath={busyPath} />,
    )
  })
}

function makeWorktree(overrides: Partial<GitWorktree> = {}): GitWorktree {
  return {
    path: '/repo/.worktrees/s-abc12345',
    name: 's-abc12345',
    kind: 'managed',
    branch: 'sess/s-abc12345',
    head: 'abc',
    managed: true,
    pinned: true,
    is_focus: false,
    ...overrides,
  }
}

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

describe('WorktreesSection (list variant)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders nothing when the project has no worktrees', () => {
    renderSection([])

    expect(container?.querySelector('[data-testid="worktree-row"]')).toBeNull()
  })

  it('lists every worktree: the local checkout, the managed session tree with its owner, and the external tree', () => {
    const main = makeWorktree({
      path: '/repo',
      name: 'repo',
      kind: 'main',
      branch: 'main',
      managed: false,
      pinned: false,
      is_focus: true,
    })
    const managed = makeWorktree({ session_id: 'sess-1', session_name: 'Refactor loop' })
    const external = makeWorktree({
      path: '/elsewhere/ext',
      name: 'ext',
      kind: 'external',
      branch: 'ext-branch',
      managed: false,
      pinned: false,
    })
    renderSection([main, managed, external])

    const rows = Array.from(container!.querySelectorAll('[data-testid="worktree-row"]'))
    expect(rows).toHaveLength(3)

    // The checkout is labeled "Local checkout"; its row carries the focus mark.
    expect(rows[0]!.textContent).toContain('Local checkout')
    expect(rows[0]!.textContent).toContain('main')
    expect(rows[0]!.querySelector('[aria-label="Focused"]')).not.toBeNull()

    // The managed tree names its tree, branch, owning session, and pin.
    expect(rows[1]!.textContent).toContain('s-abc12345')
    expect(rows[1]!.textContent).toContain('sess/s-abc12345')
    expect(rows[1]!.textContent).toContain('Refactor loop')
    expect(rows[1]!.querySelector('[aria-label="Pinned branch"]')).not.toBeNull()
    expect(rows[1]!.querySelector('[aria-label="Focused"]')).toBeNull()

    // The external tree is listed without pin or owner.
    expect(rows[2]!.textContent).toContain('ext')
    expect(rows[2]!.querySelector('[aria-label="Pinned branch"]')).toBeNull()
  })

  it('selecting a row switches the focus to that worktree', () => {
    const onSelect = vi.fn()
    const tree = makeWorktree()
    renderSection([tree], onSelect)

    const row = container!.querySelector<HTMLButtonElement>('[data-testid="worktree-row"]')!
    act(() => {
      row.click()
    })

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(tree)
  })

  it('shows a locked marker for locked trees and a spinner for the in-flight switch', () => {
    const locked = makeWorktree({ locked: true })
    const busy = makeWorktree({ path: '/repo/.worktrees/s-busy0000', name: 's-busy0000' })
    renderSection([locked, busy], vi.fn(), '/repo/.worktrees/s-busy0000')

    const rows = Array.from(container!.querySelectorAll('[data-testid="worktree-row"]'))
    expect(rows[0]!.querySelector('[aria-label="Locked"]')).not.toBeNull()
    // The busy row is disabled while its focus switch is in flight.
    expect((rows[1]! as HTMLButtonElement).disabled).toBe(true)
    expect((rows[0]! as HTMLButtonElement).disabled).toBe(false)
  })
})
