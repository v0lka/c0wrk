// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, createRef, type RefObject } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { GitOperationPopover } from './GitOperationPopover'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root
let triggerRef: RefObject<HTMLButtonElement | null>
let panelRef: RefObject<HTMLDivElement | null>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  triggerRef = createRef<HTMLButtonElement>()
  panelRef = createRef<HTMLDivElement>()
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

function render(rec: GitOperationRecord | undefined): void {
  act(() => {
    root.render(
      <div>
        <button ref={triggerRef} type="button">
          trigger
        </button>
        <GitOperationPopover
          record={rec}
          id="test-log"
          triggerRef={triggerRef}
          panelRef={panelRef}
        />
      </div>,
    )
  })
}

// The panel is portaled to document.body, so it is NOT under `container`.
function panel(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="dialog"]')
  expect(el).not.toBeNull()
  return el!
}

function header(): HTMLElement {
  const el = panel().querySelector<HTMLElement>('span')
  expect(el).not.toBeNull()
  return el!
}

function pre(): HTMLElement {
  const el = panel().querySelector<HTMLElement>('pre')
  expect(el).not.toBeNull()
  return el!
}

describe('GitOperationPopover — placement (zoom-safe, clipped-ancestor-proof)', () => {
  it('is portaled to document.body and fixed-positioned (not an in-flow anchored panel)', () => {
    render(record())
    const el = panel()
    // Portaled out of the component tree, so the Git panel's overflow-hidden
    // ancestor can never clip it or scroll under it.
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)
    expect(el.parentElement).toBe(document.body)
    // Fixed positioning is applied via the computed style, not `absolute
    // bottom-full left-0` classes (which would be clipped/scroll the panel).
    expect(el.style.position).toBe('fixed')
    expect(el.className).not.toContain('absolute')
    expect(el.className).not.toContain('bottom-full')
  })

  it('is layered above the app and capped so it never leaves the window', () => {
    render(record())
    const el = panel()
    expect(el.style.zIndex).toBe('50')
    // Height/width come from the layout-px placement computation (no raw
    // viewport units), and the panel is capped in absolute (layout) px.
    expect(el.style.maxHeight).not.toBe('')
    expect(el.style.width).not.toBe('')
  })

  it('matches the app tooltip surface (bg-background), not the darker popover', () => {
    render(record())
    const cls = panel().className
    expect(cls).toContain('bg-background')
    expect(cls).not.toContain('bg-popover')
  })

  it('scrolls its body with the custom scrollbar', () => {
    render(record())
    const cls = pre().className
    expect(cls).toContain('custom-scrollbar')
    expect(cls).toContain('overflow-auto')
  })
})

describe('GitOperationPopover — header status and output', () => {
  it('shows a green header and the captured output on success', () => {
    render(record({ ok: true, label: 'Pull', output: 'Already up to date.' }))
    expect(header().textContent).toBe('Pull')
    expect(header().className).toContain('text-success')
    expect(pre().textContent).toContain('Already up to date.')
    expect(panel().querySelector('.text-success')).not.toBeNull()
  })

  it('shows a red header and the failure message on error', () => {
    render(record({ ok: false, label: 'Push', output: '', error: 'rejected: non-fast-forward' }))
    expect(header().textContent).toBe('Push')
    expect(header().className).toContain('text-destructive')
    expect(pre().textContent).toContain('rejected: non-fast-forward')
  })

  it('falls back to "No output" when a successful op captured nothing', () => {
    render(record({ ok: true, output: '' }))
    expect(pre().textContent).toBe('No output')
  })

  it('renders a neutral empty state when there is no record', () => {
    render(undefined)
    expect(header().textContent).toBe('No git operations yet')
    expect(header().className).toContain('text-muted-foreground')
    expect(pre().textContent).toBe('No output')
  })
})
