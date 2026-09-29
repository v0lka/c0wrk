// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { UserConfirmDangerDialog } from './UserConfirmDangerDialog'

// Radix layers measure/observe elements; jsdom lacks ResizeObserver.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let container: HTMLDivElement
let root: Root
let previousRoot: Root | null = null

beforeEach(() => {
  // Unmount before wiping document.body: the dialog portals into body.
  if (previousRoot) {
    act(() => {
      previousRoot?.unmount()
    })
    previousRoot = null
  }
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  previousRoot = root
})

const render = (props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) =>
  act(async () => {
    root.render(<UserConfirmDangerDialog {...props} />)
  })

describe('UserConfirmDangerDialog', () => {
  it('renders the danger copy and both actions while open', async () => {
    await render({ open: true, onOpenChange: vi.fn(), onConfirm: vi.fn() })

    const dlg = document.querySelector('[data-testid="user-confirm-danger-dialog"]')
    expect(dlg).not.toBeNull()
    expect(dlg?.textContent).toContain('Enable unattended confirm execution?')
    expect(
      document.querySelector('[data-testid="user-confirm-danger-note"]')?.textContent,
    ).toContain('execute without confirmation')
    expect(document.querySelector('[data-testid="user-confirm-danger-confirm"]')).not.toBeNull()
    expect(document.querySelector('[data-testid="user-confirm-danger-cancel"]')).not.toBeNull()
  })

  it('renders nothing while closed', async () => {
    await render({ open: false, onOpenChange: vi.fn(), onConfirm: vi.fn() })
    expect(document.querySelector('[data-testid="user-confirm-danger-dialog"]')).toBeNull()
  })

  it('the destructive confirm button invokes onConfirm only', async () => {
    const onOpenChange = vi.fn()
    const onConfirm = vi.fn()
    await render({ open: true, onOpenChange, onConfirm })

    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="user-confirm-danger-confirm"]')
    expect(confirm?.getAttribute('data-variant')).toBe('destructive')
    await act(async () => {
      confirm?.click()
    })

    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  it('Cancel requests a dismissal and never confirms', async () => {
    const onOpenChange = vi.fn()
    const onConfirm = vi.fn()
    await render({ open: true, onOpenChange, onConfirm })

    const cancel = document.querySelector<HTMLButtonElement>('[data-testid="user-confirm-danger-cancel"]')
    await act(async () => {
      cancel?.click()
    })

    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(onConfirm).not.toHaveBeenCalled()
  })
})
