// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { WorktreeDeleteConfirmDialog } from './WorktreeDeleteConfirmDialog'
import type { PendingWorktreeDelete } from '@/hooks/useWorktreeActions'
import type { GitWorktree } from '@/types/models'
import { assertTokenBreaks } from '@/test/wrapAssertions'

let onConfirm: (mode: 'safe' | 'force', unlock: boolean) => void
let onCancel: () => void
let root: Root
let container: HTMLDivElement

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

function renderDialog(
  pending: PendingWorktreeDelete | null,
  error: string | null = null,
): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(
      <WorktreeDeleteConfirmDialog
        pending={pending}
        error={error}
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    )
  })
}

// Radix Dialog portals content into document.body.
function allButtons(): HTMLButtonElement[] {
  return Array.from(document.body.querySelectorAll('button'))
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return allButtons().find((b) => b.textContent?.includes(text))
}

function unlockCheckbox(): HTMLInputElement {
  return document.body.querySelector('[data-testid="worktree-unlock-checkbox"]') as HTMLInputElement
}

beforeEach(() => {
  onConfirm = vi.fn<(mode: 'safe' | 'force', unlock: boolean) => void>()
  onCancel = vi.fn<() => void>()
  document.body.innerHTML = ''
})

afterEach(() => {
  if (root) {
    act(() => {
      root.unmount()
    })
  }
  if (container) container.remove()
  document.body.innerHTML = ''
})

describe('WorktreeDeleteConfirmDialog', () => {
  it('renders nothing when there is no pending action', () => {
    renderDialog(null)
    expect(document.body.textContent).not.toContain('Delete worktree?')
    expect(allButtons().length).toBe(0)
  })

  it('names the tree, its branch and its path', () => {
    renderDialog({ worktree: makeWorktree() })
    expect(document.body.textContent).toContain('Delete worktree?')
    expect(document.body.textContent).toContain('s-abc12345')
    expect(document.body.textContent).toContain('sess/s-abc12345')
    expect(document.body.textContent).toContain('/repo/.worktrees/s-abc12345')
  })

  it('wraps long name and branch tokens at separators without altering the text', () => {
    const longName = 'an-extremely-long-managed-worktree-session-name'
    const longBranch = 'feature/very/deeply/nested/long-branch-name'
    renderDialog({
      worktree: makeWorktree({
        name: longName,
        branch: longBranch,
        path: `/repo/.worktrees/${longName}`,
      }),
    })
    // The name token has no separators: text stays char-identical, zero <wbr>.
    assertTokenBreaks(longName)
    // The branch token breaks after every `/` separator.
    assertTokenBreaks(longBranch)
    expect(document.body.textContent).toContain(
      `${longName}${longBranch}`,
    )
  })

  it('offers safe and force delete plus cancel', () => {
    renderDialog({ worktree: makeWorktree() })
    expect(buttonByText('Cancel')).toBeTruthy()
    expect(buttonByText('Force delete')).toBeTruthy()
    expect(buttonByText('Delete')).toBeTruthy()
  })

  it('calls onConfirm(safe, false) for the Delete button', () => {
    renderDialog({ worktree: makeWorktree() })
    act(() => {
      buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('safe', false)
  })

  it('calls onConfirm(force, false) for the Force delete button', () => {
    renderDialog({ worktree: makeWorktree() })
    act(() => {
      buttonByText('Force delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('force', false)
  })

  it('calls onCancel for the Cancel button', () => {
    renderDialog({ worktree: makeWorktree() })
    act(() => {
      buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onCancel).toHaveBeenCalled()
  })

  it('shows no unlock option for an unlocked tree', () => {
    renderDialog({ worktree: makeWorktree({ locked: false }) })
    expect(document.body.querySelector('[data-testid="worktree-unlock-row"]')).toBeNull()
  })

  it('offers an unlock consent for a locked tree, off by default', () => {
    renderDialog({ worktree: makeWorktree({ locked: true }) })
    const box = unlockCheckbox()
    expect(box).toBeTruthy()
    expect(box.checked).toBe(false)

    act(() => {
      buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    // Unchecked consent → no unlock authorization.
    expect(onConfirm).toHaveBeenCalledWith('safe', false)
  })

  it('passes a ticked unlock consent through to onConfirm', () => {
    renderDialog({ worktree: makeWorktree({ locked: true }) })
    act(() => {
      unlockCheckbox().click()
    })
    expect(unlockCheckbox().checked).toBe(true)

    act(() => {
      buttonByText('Force delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('force', true)
  })

  it('resets the unlock consent when a different tree is confirmed-pending', () => {
    const first = makeWorktree({ path: '/repo/.worktrees/s-aaa00000', name: 's-aaa00000', locked: true })
    const second = makeWorktree({ path: '/repo/.worktrees/s-bbb00000', name: 's-bbb00000', locked: true })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)

    act(() => {
      root.render(
        <WorktreeDeleteConfirmDialog
          pending={{ worktree: first }}
          error={null}
          onConfirm={onConfirm}
          onCancel={onCancel}
        />,
      )
    })
    act(() => {
      unlockCheckbox().click()
    })
    expect(unlockCheckbox().checked).toBe(true)

    act(() => {
      root.render(
        <WorktreeDeleteConfirmDialog
          pending={{ worktree: second }}
          error={null}
          onConfirm={onConfirm}
          onCancel={onCancel}
        />,
      )
    })
    expect(unlockCheckbox().checked).toBe(false)
  })

  it('shows the backend refusal inline, with a force hint for a dirty tree', () => {
    renderDialog(
      { worktree: makeWorktree() },
      'worktree has uncommitted changes: M frontend/src/api/git.ts',
    )
    expect(document.body.textContent).toContain('worktree has uncommitted changes')
    expect(document.body.textContent).toContain('Force delete discards the uncommitted changes.')
  })

  it('shows a non-dirty refusal without the force hint', () => {
    renderDialog({ worktree: makeWorktree() }, 'git worktree remove failed: fatal: boom')
    expect(document.body.textContent).toContain('git worktree remove failed: fatal: boom')
    expect(document.body.textContent).not.toContain('Force delete discards the uncommitted changes.')
  })
})
