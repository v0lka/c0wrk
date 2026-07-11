// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

import { ContextFillStatus } from './ContextFillStatus'

describe('ContextFillStatus', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  const render = (props: {
    percent: number | undefined
    usedTokens?: number
    maxTokens?: number
  }) =>
    act(() => {
      root.render(<ContextFillStatus {...props} />)
    })

  it('hides when percent is undefined', () => {
    render({ percent: undefined })
    expect(container.textContent).toBe('')
  })

  it('hides while 0 tokens are used (usedTokens 0)', () => {
    render({ percent: 0, usedTokens: 0, maxTokens: 100000 })
    expect(container.textContent).toBe('')
  })

  it('hides on load when percent is 0 and no token counts', () => {
    render({ percent: 0 })
    expect(container.textContent).toBe('')
  })

  it('renders "N of M" tooltip when token counts are available', () => {
    render({ percent: 50, usedTokens: 50000, maxTokens: 100000 })
    const el = container.firstElementChild as HTMLElement
    expect(el).not.toBeNull()
    expect(el.getAttribute('title')).toBe('Context fill: 50.0K of 100.0K')
    expect(el.getAttribute('aria-label')).toBe('Context fill: 50.0K of 100.0K')
    // Percentage label still visible.
    expect(container.textContent).toContain('50%')
  })

  it('renders the reasoning-style (BrainCircuit) icon', () => {
    render({ percent: 50, usedTokens: 50000, maxTokens: 100000 })
    const el = container.firstElementChild as HTMLElement
    expect(el.querySelector('svg')).not.toBeNull()
  })

  it('falls back to percentage tooltip when maxTokens is unknown', () => {
    render({ percent: 42 })
    const el = container.firstElementChild as HTMLElement
    expect(el).not.toBeNull()
    expect(el.getAttribute('title')).toBe('Context fill: 42%')
  })

  it('shows on load with percent > 0 and no token counts', () => {
    render({ percent: 30 })
    expect(container.textContent).toContain('30%')
  })
})
