// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { GitOperationButton } from './GitOperationButton'
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

/** Build a record with sane defaults, overridable per test. */
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

function render(props: {
  record?: GitOperationRecord | undefined
  busy?: boolean
}): void {
  act(() => {
    root.render(<GitOperationButton record={props.record} busy={props.busy ?? false} />)
  })
}

function button(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Git operation log"]')
  expect(el).not.toBeNull()
  return el!
}

describe('GitOperationButton — outcome tint', () => {
  it('tints green after a successful operation', () => {
    render({ record: record({ ok: true }) })
    expect(button().className).toContain('text-success')
    expect(button().className).not.toContain('text-destructive')
    expect(button().className).not.toContain('text-muted-foreground')
  })

  it('tints red after a failed operation', () => {
    render({ record: record({ ok: false, error: 'boom' }) })
    expect(button().className).toContain('text-destructive')
    expect(button().className).not.toContain('text-success')
  })

  it('is neutral when there is no record', () => {
    render({ record: undefined })
    expect(button().className).toContain('text-muted-foreground')
    expect(button().className).not.toContain('text-success')
    expect(button().className).not.toContain('text-destructive')
  })

  it('is neutral once the result has been acknowledged', () => {
    render({ record: record({ ok: true, acknowledged: true }) })
    expect(button().className).toContain('text-muted-foreground')
    expect(button().className).not.toContain('text-success')
  })

  it('shows a spinner and neutralizes while busy, but stays clickable', () => {
    render({ record: record({ ok: true }), busy: true })
    // The log of the previous result stays reachable during an operation.
    expect(button().disabled).toBe(false)
    expect(container.querySelector('.animate-spin')).not.toBeNull()
    expect(button().className).toContain('text-muted-foreground')
    expect(button().className).not.toContain('text-success')
  })
})

describe('GitOperationButton — design contract', () => {
  it('owns no trigger state: no expanded/controls/haspopup aria of its own', () => {
    render({ record: record() })
    // The trigger contract — aria-haspopup/aria-expanded/aria-controls and the
    // toggling onClick — belongs to the surrounding Radix Popover, composed
    // through the trailing props spread when rendered as `PopoverTrigger
    // asChild` (pinned against the real composition in
    // GitOperationPopover.test.tsx). Standalone the button fakes none of it,
    // so a Radix-composed onClick can never double-fire a manual toggle.
    expect(button().getAttribute('aria-expanded')).toBeNull()
    expect(button().getAttribute('aria-controls')).toBeNull()
    expect(button().getAttribute('aria-haspopup')).toBeNull()
  })
})
