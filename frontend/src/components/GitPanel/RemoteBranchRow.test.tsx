// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'
import { RemoteBranchRow } from './RemoteBranchRow'
import type { Branch } from '@/types/models'

function makeBranch(overrides: Partial<Branch> = {}): Branch {
  return { name: 'origin/feature/x', is_current: false, kind: 'remote', upstream: '', ...overrides }
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

function buttons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

function row(container: HTMLElement): HTMLElement {
  return container.querySelector('[role="button"]') as HTMLElement
}

const defaultCallbacks = {
  onCheckoutRemote: vi.fn(),
  onDeleteRemote: vi.fn(),
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('RemoteBranchRow', () => {
  it('renders the full remote-tracking ref', () => {
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    expect(container.textContent).toContain('origin/feature/x')
  })

  it('renders a single delete-remote hover action icon', () => {
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...defaultCallbacks} />,
    )
    const btns = buttons(container)
    expect(btns.length).toBe(1)
    expect(btns[0]!.querySelector('svg')).not.toBeNull()
  })

  it('calls onCheckoutRemote with the full ref on click', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...callbacks} />,
    )
    act(() => {
      row(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onCheckoutRemote).toHaveBeenCalledWith('origin/feature/x')
  })

  it('splits remote and short name for the delete action', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight={null} disabled={false} {...callbacks} />,
    )
    act(() => {
      buttons(container)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onDeleteRemote).toHaveBeenCalledWith('feature/x', 'origin')
    expect(callbacks.onCheckoutRemote).not.toHaveBeenCalled()
  })

  it('handles nested branch names (everything after the first slash)', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <RemoteBranchRow
        branch={makeBranch({ name: 'origin/feature/deep/x' })}
        inFlight={null}
        disabled={false}
        {...callbacks}
      />,
    )
    act(() => {
      buttons(container)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onDeleteRemote).toHaveBeenCalledWith('feature/deep/x', 'origin')
  })

  it('disables checkout and delete while another operation is in flight', () => {
    const callbacks = { ...defaultCallbacks }
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight={null} disabled={true} {...callbacks} />,
    )
    act(() => {
      row(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(callbacks.onCheckoutRemote).not.toHaveBeenCalled()
    expect(buttons(container)[0]!.disabled).toBe(true)
  })

  it('shows a spinner while the remote checkout is in flight', () => {
    const { container } = render(
      <RemoteBranchRow branch={makeBranch()} inFlight="checkoutRemote" disabled={false} {...defaultCallbacks} />,
    )
    // The trailing spinner icon is rendered adjacent to the name.
    expect(container.querySelector('svg.animate-spin')).not.toBeNull()
  })
})
