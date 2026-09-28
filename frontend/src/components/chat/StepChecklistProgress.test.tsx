// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { StepChecklistProgress } from './StepChecklistProgress'

describe('StepChecklistProgress', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (total: number, completed: number) =>
    act(() => {
      root.render(<StepChecklistProgress total={total} completed={completed} accent="info" />)
    })

  const bar = () => container.querySelector('[role="progressbar"]')

  it('exposes progressbar semantics matching the completed/total props', () => {
    render(4, 2)
    const el = bar()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('aria-valuenow')).toBe('2')
    expect(el!.getAttribute('aria-valuemin')).toBe('0')
    expect(el!.getAttribute('aria-valuemax')).toBe('4')
    expect(el!.getAttribute('aria-label')).toBe('Checklist: 2 of 4')
  })

  it('clamps completed above total into the aria-valuenow', () => {
    render(3, 7)
    expect(bar()!.getAttribute('aria-valuenow')).toBe('3')
  })

  it('renders nothing for a non-positive total', () => {
    render(0, 0)
    expect(bar()).toBeNull()
    expect(container.querySelector('span')).toBeNull()
  })
})
