// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { GitOperationPopover } from './GitOperationPopover'
import { GitOperationButton } from './GitOperationButton'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide.
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
let openState: boolean
let lastRecord: GitOperationRecord | undefined

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  openState = false
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function record(overrides: Partial<GitOperationRecord> = {}): GitOperationRecord {
  return {
    kind: 'push',
    label: 'Push',
    ok: true,
    output: '',
    error: null,
    at: 1,
    acknowledged: false,
    ...overrides,
  }
}

/** Rerender with the given record/open state. The trigger is the real
 *  `GitOperationButton` — the footer's exact trigger — so the aria/interaction
 *  assertions below exercise the production `PopoverTrigger asChild` Slot
 *  composition, not a stand-in button. */
function render(rec: GitOperationRecord | undefined, open = openState): void {
  lastRecord = rec
  act(() => {
    root.render(
      <GitOperationPopover record={rec} open={open} onOpenChange={setOpen}>
        <GitOperationButton record={rec} busy={false} />
      </GitOperationPopover>,
    )
  })
}

/** Controlled-open mirror: exactly what the footer does with its own state. */
function setOpen(next: boolean): void {
  openState = next
  render(lastRecord, next)
}

function content(): HTMLElement {
  const el = document.body.querySelector<HTMLElement>('[data-slot="popover-content"]')
  expect(el).not.toBeNull()
  return el!
}

function trigger(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button')
  expect(el).not.toBeNull()
  return el!
}

function header(): HTMLElement {
  const el = content().querySelector<HTMLElement>('span')
  expect(el).not.toBeNull()
  return el!
}

function pre(): HTMLElement {
  const el = content().querySelector<HTMLElement>('pre')
  expect(el).not.toBeNull()
  return el!
}

/** Flush Radix's macrotask-queued listeners (outside-interaction, Presence). */
async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 10))
  })
}

describe('GitOperationPopover — portal placement (clipped-ancestor-proof)', () => {
  it('mounts the panel through the Radix portal outside the React tree', () => {
    render(record(), true)
    const el = content()
    // Portaled out of the component tree, so the Git panel's overflow-hidden
    // ancestor can never clip it or scroll under it.
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)
    // Radix owns the panel as a dialog.
    expect(el.getAttribute('role')).toBe('dialog')
    expect(el.getAttribute('aria-label')).toBe('Git operation log')
    expect(el.id).not.toBe('')
  })

  it('carries no manual positioning — geometry belongs to Radix/floating-ui', () => {
    render(record(), true)
    const el = content()
    // No side-pinning classes (which would be clipped / scroll the panel).
    for (const cls of ['absolute', 'fixed', 'top-0', 'left-0', 'bottom-full', 'left-full']) {
      expect(el.className).not.toContain(cls)
    }
    // No inline geometry either: sizing is token/class-driven (w-96 and the
    // Radix available-space caps), never computed px.
    expect(el.style.width).toBe('')
    expect(el.style.maxHeight).toBe('')
    expect(el.className).toContain('w-96')
    expect(el.className).toContain('max-h-[min(var(--radix-popover-content-available-height),22.5rem)]')
    expect(el.className).toContain('max-w-(--radix-popover-content-available-width)')
    // Anchored above, left-aligned to the trigger; layered above the app.
    expect(el.getAttribute('data-side')).toBe('top')
    expect(el.getAttribute('data-align')).toBe('start')
    expect(el.className).toContain('z-50')
  })

  it('matches the app tooltip surface (bg-background), not the darker popover', () => {
    render(record(), true)
    const cls = content().className
    expect(cls).toContain('bg-background')
    expect(cls).not.toContain('bg-popover')
  })

  it('scrolls its body with the custom scrollbar', () => {
    render(record(), true)
    const cls = pre().className
    expect(cls).toContain('custom-scrollbar')
    expect(cls).toContain('overflow-auto')
  })
})

describe('GitOperationPopover — aria contract', () => {
  it('wires the trigger to the panel: aria-controls, aria-expanded, haspopup', () => {
    render(record(), true)
    const el = content()
    expect(trigger().getAttribute('aria-expanded')).toBe('true')
    expect(trigger().getAttribute('aria-haspopup')).toBe('dialog')
    // The trigger's `aria-controls` names the portaled panel's id.
    expect(trigger().getAttribute('aria-controls')).toBe(el.id)
    expect(el.getAttribute('data-state')).toBe('open')
  })

  it('reflects the closed state on the trigger', () => {
    render(record(), false)
    expect(document.body.querySelector('[data-slot="popover-content"]')).toBeNull()
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
    expect(trigger().getAttribute('data-state')).toBe('closed')
  })
})

describe('GitOperationPopover — trigger interaction', () => {
  it('opens on trigger click and closes on Escape, restoring focus to the trigger', async () => {
    render(record(), false)
    expect(document.body.querySelector('[data-slot="popover-content"]')).toBeNull()

    await act(async () => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await settle()
    })
    expect(document.body.querySelector('[data-slot="popover-content"]')).not.toBeNull()
    expect(trigger().getAttribute('data-state')).toBe('open')

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await settle()
    })
    expect(document.body.querySelector('[data-slot="popover-content"]')).toBeNull()
    // Radix-managed focus: back on the trigger after the dismiss.
    expect(document.activeElement).toBe(trigger())
  })
})

describe('GitOperationPopover — header status and output', () => {
  it('shows a green header and the captured output on success', () => {
    render(record({ ok: true, label: 'Pull', output: 'Already up to date.' }), true)
    expect(header().textContent).toBe('Pull')
    expect(header().className).toContain('text-success')
    expect(pre().textContent).toContain('Already up to date.')
    expect(content().querySelector('.text-success')).not.toBeNull()
  })

  it('shows a red header and the failure message on error', () => {
    render(record({ ok: false, label: 'Push', output: '', error: 'rejected: non-fast-forward' }), true)
    expect(header().textContent).toBe('Push')
    expect(header().className).toContain('text-destructive')
    expect(pre().textContent).toContain('rejected: non-fast-forward')
  })

  it('falls back to "No output" when a successful op captured nothing', () => {
    render(record({ ok: true, output: '' }), true)
    expect(pre().textContent).toBe('No output')
  })

  it('renders a neutral empty state when there is no record', () => {
    render(undefined, true)
    expect(header().textContent).toBe('No git operations yet')
    expect(header().className).toContain('text-muted-foreground')
    expect(pre().textContent).toBe('No output')
  })
})
