// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { GitOperationPopover } from './GitOperationPopover'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
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
    root.render(<GitOperationPopover record={rec} />)
  })
}

function panel(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[role="dialog"]')
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

describe('GitOperationPopover — placement (zoom-safe)', () => {
  it('anchors above-right, is layered, and is capped in --ui-vh units', () => {
    render(record())
    const cls = panel().className
    expect(cls).toContain('absolute')
    expect(cls).toContain('bottom-full')
    expect(cls).toContain('right-0')
    expect(cls).toContain('z-50')
    expect(cls).toContain('w-96')
    expect(cls).toContain('bg-popover')
    expect(cls).toContain('max-h-[calc(var(--ui-vh)*0.5)]')
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
    expect(container.querySelector('.text-success')).not.toBeNull()
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
