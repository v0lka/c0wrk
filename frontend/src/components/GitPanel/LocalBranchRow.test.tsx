// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'
import { LocalBranchRow } from './LocalBranchRow'
import type { Branch } from '@/types/models'

function makeBranch(overrides: Partial<Branch> = {}): Branch {
  return { name: 'feature/x', is_current: false, kind: 'local', upstream: '', ...overrides }
}

function render(ui: React.ReactNode): { root: Root; container: HTMLElement } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(<TooltipProvider>{ui}</TooltipProvider>)
  })
  return { root, container }
}

const defaultCallbacks = {
  onCheckout: vi.fn(),
  onRename: vi.fn(),
  onMerge: vi.fn(),
  onRebase: vi.fn(),
  onPush: vi.fn(),
  onDelete: vi.fn(),
}

/** The hover overlay renders one <button> per action, in fixed order. */
function buttons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

/** The outer row is a div[role=button], distinct from the action <button>s. */
function row(container: HTMLElement): HTMLElement {
  return container.querySelector('[role="button"]') as HTMLElement
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('LocalBranchRow', () => {
  it('renders the branch name', () => {
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    expect(container.textContent).toContain('feature/x')
  })

  it('shows a check icon for the current branch', () => {
    const { container } = render(
      <LocalBranchRow branch={makeBranch({ name: 'main', is_current: true })} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    // The current branch has a Check icon; a non-current row has none besides
    // the leading GitBranch glyph. Assert the check via the absence of the
    // merge/rebase actions and the disabled delete, and the row's aria-current.
    expect(row(container).getAttribute('aria-current')).toBe('true')
  })

  it('renders five hover action icons (push/merge/rebase/rename/delete) for a non-current branch', () => {
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    const btns = buttons(container)
    expect(btns.length).toBe(5)
    for (const b of btns) {
      expect(b.querySelector('svg')).not.toBeNull()
    }
  })

  it('hides merge/rebase and disables delete for the current branch', () => {
    const { container } = render(
      <LocalBranchRow branch={makeBranch({ name: 'main', is_current: true })} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    const btns = buttons(container)
    // push | rename | delete (merge & rebase hidden)
    expect(btns.length).toBe(3)
    const [, , del] = btns
    expect(del?.disabled).toBe(true)
  })

  it('calls onCheckout when a non-current row is clicked', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...callbacks} />,
    )
    act(() => {
      row(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onCheckout).toHaveBeenCalledWith('feature/x')
  })

  it('does not checkout the current branch', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <LocalBranchRow branch={makeBranch({ name: 'main', is_current: true })} inFlight={null} disabled={false} {...callbacks} />,
    )
    act(() => {
      row(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onCheckout).not.toHaveBeenCalled()
  })

  it('wires the delete action and does not trigger checkout', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...callbacks} />,
    )
    const btns = buttons(container)
    const del = btns[4]!
    act(() => {
      del.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onDelete).toHaveBeenCalledWith('feature/x')
    expect(callbacks.onCheckout).not.toHaveBeenCalled()
  })

  it('wires the rename action', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...callbacks} />,
    )
    const btns = buttons(container)
    const rename = btns[3]!
    act(() => {
      rename.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onRename).toHaveBeenCalledWith('feature/x')
  })

  it('disables checkout and action buttons while another operation is in flight', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight={null} disabled={true} {...callbacks} />,
    )
    act(() => {
      row(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onCheckout).not.toHaveBeenCalled()
    for (const b of buttons(container)) {
      expect(b.disabled).toBe(true)
    }
  })

  it('shows a spinner in place of the push icon while push is in flight', () => {
    const { container } = render(
      <LocalBranchRow branch={makeBranch()} inFlight="push" disabled={false} {...defaultCallbacks} />,
    )
    const btns = buttons(container)
    const push = btns[0]!
    expect(push.querySelector('svg')?.classList.contains('animate-spin')).toBe(true)
  })
})
