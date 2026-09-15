// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { createElement } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { ErrorBoundary } from '@/components/ErrorBoundary'

function Boom({ explode }: { explode: boolean }) {
  if (explode) throw new Error('boom')
  return createElement('div', null, 'OK')
}

function boundary(props: { resetKeys?: readonly unknown[]; explode: boolean }) {
  return createElement(ErrorBoundary, {
    resetKeys: props.resetKeys,
    fallback: createElement('div', null, 'FALLBACK'),
    children: createElement(Boom, { explode: props.explode }),
  })
}

let container: HTMLDivElement
let root: Root

function render(node: React.ReactNode) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(node)
  })
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

describe('ErrorBoundary', () => {
  it('renders the fallback after a child throws', () => {
    render(boundary({ explode: true }))
    expect(container.textContent).toContain('FALLBACK')
  })

  it('does NOT reset without resetKeys changing (a caught error sticks)', () => {
    render(boundary({ explode: true }))
    act(() => {
      root.render(boundary({ explode: false }))
    })
    // Children now render fine, but React never retries an errored boundary.
    expect(container.textContent).toContain('FALLBACK')
    expect(container.textContent).not.toContain('OK')
  })

  it('resets when a resetKey changes, re-rendering the (now healthy) children', () => {
    render(boundary({ resetKeys: ['a'], explode: true }))
    expect(container.textContent).toContain('FALLBACK')

    act(() => {
      root.render(boundary({ resetKeys: ['b'], explode: false }))
    })
    expect(container.textContent).toContain('OK')
    expect(container.textContent).not.toContain('FALLBACK')
  })
})
