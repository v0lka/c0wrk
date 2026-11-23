// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { Popover, PopoverContent, PopoverTrigger } from './popover'

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

function render(props: Partial<Parameters<typeof PopoverContent>[0]> = {}) {
  act(() => {
    root.render(
      <Popover open>
        <PopoverTrigger>Open</PopoverTrigger>
        <PopoverContent data-testid="subject" {...props}>
          <div>Body</div>
        </PopoverContent>
      </Popover>,
    )
  })
}

function content(): HTMLElement {
  const el = document.body.querySelector<HTMLElement>(
    '[data-slot="popover-content"]',
  )
  expect(el).not.toBeNull()
  return el!
}

/** Popover.Content rendered inside the wrapper's own markup — for the
 *  controlled-open render helper above the trigger is the only button. */
function trigger(): HTMLButtonElement {
  const el = container.querySelector('button')
  expect(el).not.toBeNull()
  return el!
}

describe('PopoverContent — portal placement', () => {
  it('mounts the content through a Radix portal outside the React tree', () => {
    render()
    const el = content()
    // Portaled out of the component tree, so overflow-hidden/transform
    // ancestors (zoom shell, panels) can never clip or offset the panel.
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)
  })

  it('adds no own inline positioning or side-pinning classes', () => {
    render()
    const el = content()
    // Geometry belongs to Radix/floating-ui (with the global zoom patch);
    // the wrapper must not pin the panel with its own positioning.
    for (const cls of ['absolute', 'fixed', 'top-0', 'left-0', 'bottom-full', 'left-full']) {
      expect(el.className).not.toContain(cls)
    }
  })

  it('forwards Radix content props (collisionPadding, align, sideOffset)', () => {
    expect(() =>
      render({ collisionPadding: 16, align: 'start', sideOffset: 8 }),
    ).not.toThrow()
    expect(content()).not.toBeNull()
  })
})

describe('PopoverContent — skin consistent with the dropdown-menu wrapper', () => {
  it('carries the same popover surface tokens and stacking layer', () => {
    render()
    const cls = content().className
    expect(cls).toContain('z-50')
    expect(cls).toContain('bg-popover')
    expect(cls).toContain('text-popover-foreground')
    expect(cls).toContain('rounded-md')
    expect(cls).toContain('border')
    expect(cls).toContain('shadow-md')
  })

  it('scrolls long content with the custom scrollbar against the Radix height cap', () => {
    render()
    const cls = content().className
    expect(cls).toContain('custom-scrollbar')
    expect(cls).toContain('overflow-y-auto')
    expect(cls).toContain('max-h-(--radix-popover-content-available-height)')
  })

  it('merges caller classes over the defaults (tailwind-merge)', () => {
    render({ className: 'p-4 min-w-[12rem]' })
    const cls = content().className
    expect(cls).toContain('p-4')
    expect(cls).toContain('min-w-[12rem]')
    expect(cls).not.toContain('p-1')
    expect(cls).not.toContain('min-w-[8rem]')
  })
})

describe('Popover — trigger interaction', () => {
  it('opens on trigger click and closes again (uncontrolled)', async () => {
    act(() => {
      root.render(
        <Popover>
          <PopoverTrigger>Open</PopoverTrigger>
          <PopoverContent>
            <div>Body</div>
          </PopoverContent>
        </Popover>,
      )
    })
    expect(document.body.querySelector('[data-slot="popover-content"]')).toBeNull()

    // The async act flushes the macrotask queue: Radix installs its
    // document-level outside-interaction listener via setTimeout(0) after
    // the content mounts.
    await act(async () => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    expect(
      document.body.querySelector('[data-slot="popover-content"]'),
    ).not.toBeNull()
    expect(
      trigger().getAttribute('data-state'),
    ).toBe('open')

    await act(async () => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    expect(document.body.querySelector('[data-slot="popover-content"]')).toBeNull()
    expect(trigger().getAttribute('data-state')).toBe('closed')
  })
})
