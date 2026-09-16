// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { PlanStepBlock } from './PlanStepBlock'
import { BookmarkableContext } from './BookmarkableContext'
import type { DisplayItem } from '@/types/messages'

type PlanStepItem = Extract<DisplayItem, { kind: 'plan_step' }>

function makeStep(overrides: Partial<PlanStepItem> = {}): PlanStepItem {
  return {
    kind: 'plan_step',
    id: 'step-1',
    stepId: 'step_1',
    stepNum: 1,
    title: 'Do the thing',
    status: 'running',
    children: [],
    ...overrides,
  }
}

const CHILD: DisplayItem = { kind: 'memory_read', id: 'mem-1', content: 'BODY_MARKER' }

describe('PlanStepBlock', () => {
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

  const render = (item: PlanStepItem) =>
    act(() => {
      // bookmarkable=false keeps the test free of the hover/bookmark stores.
      root.render(
        <BookmarkableContext.Provider value={false}>
          <PlanStepBlock item={item} />
        </BookmarkableContext.Provider>,
      )
    })

  // Radix keeps CollapsibleContent mounted and toggles `data-state`
  // (open/closed) + `hidden`, so assert on that rather than on presence.
  const state = () =>
    container.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')
  const trigger = () => container.querySelector('[data-slot="collapsible-trigger"]') as HTMLElement

  it('renders collapsed by default while running (no auto-open)', () => {
    render(makeStep({ status: 'running', children: [CHILD] }))
    expect(state()).toBe('closed')
  })

  it('renders collapsed by default once completed', () => {
    render(makeStep({ status: 'completed', children: [CHILD] }))
    expect(state()).toBe('closed')
  })

  it('renders collapsed by default when failed', () => {
    render(makeStep({ status: 'failed', error: 'boom', children: [CHILD] }))
    expect(state()).toBe('closed')
  })

  it('expands its body when the user clicks the header', () => {
    render(makeStep({ status: 'running', children: [CHILD] }))
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(state()).toBe('open')
  })

  // --- work-unit recovery statuses (applied by the session-load reconciliation) ---

  it('renders an interrupted step with an "— interrupted" hint', () => {
    render(makeStep({ status: 'interrupted', children: [CHILD] }))
    expect(container.textContent).toContain('interrupted')
    expect(state()).toBe('closed')
  })

  it('gives a paused step the warning status icon', () => {
    render(makeStep({ status: 'paused', children: [CHILD] }))
    expect(container.querySelector('svg.text-warning')).not.toBeNull()
  })
})
