// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { PlanStepBlock } from './PlanStepBlock'
import { BookmarkableContext } from './BookmarkableContext'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
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
    useSessionStore.setState({ activeSessionId: 'sess-1' })
    useChatStore.setState({ stepContextFill: {}, stepContextTokens: {} })
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

  // --- header cluster: checklist progress + context fill ---

  const checklist = (checked: boolean[]): DisplayItem => ({
    kind: 'checklist',
    id: 'cl-1',
    stepId: 'step_1',
    active: true,
    items: checked.map(c => ({ text: c ? 'done thing' : 'todo thing', checked: c })),
  })

  it('shows the checklist progress (icon, status-colored bar, N/M) before the context fill', () => {
    useChatStore.setState({ stepContextFill: { 'sess-1': { step_1: 42 } } })
    render(makeStep({ status: 'running', children: [CHILD, checklist([true, true, false])] }))
    const header = container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header.textContent).toContain('2/3')
    expect(header.textContent).toContain('42%')
    const checklistWrap = header.querySelector('[title="Checklist: 2 of 3"]')
    expect(checklistWrap).not.toBeNull()
    const fillWrap = header.querySelector('[title="Context fill: 42%"]')
    expect(fillWrap).not.toBeNull()
    // Order: the checklist cluster precedes the context control.
    expect(
      checklistWrap!.compareDocumentPosition(fillWrap!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // The bar follows the step's current status color (running → info blue),
    // not a fixed success green.
    expect(checklistWrap!.querySelector('.bg-info')).not.toBeNull()
    expect(checklistWrap!.querySelector('.bg-success')).toBeNull()
  })

  it('tints the checklist indicator with each status accent', () => {
    const barClass = (status: PlanStepItem['status']) => {
      render(makeStep({ status, children: [CHILD, checklist([false])] }))
      const wrap = container.querySelector('[title^="Checklist:"]')!
      return {
        success: !!wrap.querySelector('.bg-success'),
        destructive: !!wrap.querySelector('.bg-destructive'),
        warning: !!wrap.querySelector('.bg-warning'),
        muted: !!wrap.querySelector('.bg-muted-foreground'),
      }
    }
    expect(barClass('completed').success).toBe(true)
    expect(barClass('failed').destructive).toBe(true)
    expect(barClass('paused').warning).toBe(true)
    expect(barClass('interrupted').muted).toBe(true)
  })

  it('renders the context fill without a checklist (no checklist cluster)', () => {
    useChatStore.setState({ stepContextFill: { 'sess-1': { step_1: 7 } } })
    render(makeStep({ status: 'running', children: [CHILD] }))
    const header = container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header.querySelector('[title^="Checklist:"]')).toBeNull()
    expect(header.querySelector('[title="Context fill: 7%"]')).not.toBeNull()
  })

  it('renders no header cluster without checklist or fill data', () => {
    render(makeStep({ status: 'running', children: [CHILD] }))
    const header = container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header.querySelector('[title^="Checklist:"]')).toBeNull()
    expect(header.querySelector('[title^="Context fill:"]')).toBeNull()
  })

  it('shows the token counts in the context tooltip (N of M)', () => {
    useChatStore.setState({
      stepContextFill: { 'sess-1': { step_1: 50 } },
      stepContextTokens: { 'sess-1': { step_1: { used_tokens: 4000, max_tokens: 8000 } } },
    })
    render(makeStep({ status: 'running', children: [CHILD] }))
    expect(container.querySelector('[title="Context fill: 4.0K of 8.0K"]')).not.toBeNull()
  })

  it('hides the context fill when tokens are spent but percent is missing', () => {
    useChatStore.setState({ stepContextFill: {}, stepContextTokens: { 'sess-1': { step_1: { used_tokens: 1, max_tokens: 10 } } } })
    render(makeStep({ status: 'running', children: [CHILD] }))
    expect(container.querySelector('[title^="Context fill:"]')).toBeNull()
  })

  it('re-renders the header when the checklist child updates', () => {
    render(makeStep({ status: 'running', children: [CHILD, checklist([true, false])] }))
    const header = () => container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header().querySelector('[title="Checklist: 1 of 2"]')).not.toBeNull()

    // Fresh wrapper objects (what groupMessages produces on every rebuild);
    // the recursive memo comparator must see the checked flip and re-render.
    const updated = makeStep({ status: 'running', children: [CHILD, checklist([true, true])] })
    act(() => {
      root.render(
        <BookmarkableContext.Provider value={false}>
          <PlanStepBlock item={updated} />
        </BookmarkableContext.Provider>,
      )
    })
    expect(header().querySelector('[title="Checklist: 1 of 2"]')).toBeNull()
    expect(header().querySelector('[title="Checklist: 2 of 2"]')).not.toBeNull()
  })
})
