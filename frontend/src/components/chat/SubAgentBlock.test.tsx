// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SubAgentBlock } from './SubAgentBlock'
import { BookmarkableContext } from './BookmarkableContext'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { DisplayItem } from '@/types/messages'

type SubAgentItem = Extract<DisplayItem, { kind: 'subagent' }>

function makeSub(overrides: Partial<SubAgentItem> = {}): SubAgentItem {
  return {
    kind: 'subagent',
    id: 'sub-1',
    stepId: 'del_1',
    title: 'Research topic',
    status: 'running',
    children: [],
    ...overrides,
  }
}

const CHILD: DisplayItem = { kind: 'memory_read', id: 'mem-1', content: 'BODY_MARKER' }

describe('SubAgentBlock', () => {
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

  const render = (item: SubAgentItem) =>
    act(() => {
      root.render(
        <BookmarkableContext.Provider value={false}>
          <SubAgentBlock item={item} />
        </BookmarkableContext.Provider>,
      )
    })

  const state = () =>
    container.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')
  const trigger = () => container.querySelector('[data-slot="collapsible-trigger"]') as HTMLElement

  it('renders collapsed by default while running', () => {
    render(makeSub({ status: 'running', children: [CHILD] }))
    expect(state()).toBe('closed')
  })

  it('renders collapsed by default once completed', () => {
    render(makeSub({ status: 'completed', duration: 1200, children: [CHILD] }))
    expect(state()).toBe('closed')
  })

  // --- failure reason surfaced in the header (mirrors PlanStepBlock) ---

  it('renders the failure reason in the header when failed', () => {
    render(makeSub({ status: 'failed', error: 'max steps exceeded', duration: 3000 }))
    expect(container.textContent).toContain('max steps exceeded')
    // The reason is a settled header hint, not an auto-expanded body.
    expect(state()).toBe('closed')
  })

  it('does not render a stale error hint when the block completed', () => {
    render(makeSub({ status: 'completed', error: 'stale reason', duration: 3000 }))
    expect(container.textContent).not.toContain('stale reason')
  })

  it('expands its body when the user clicks the header', () => {
    render(makeSub({ status: 'completed', children: [CHILD] }))
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(state()).toBe('open')
  })

  // --- work-unit recovery statuses (applied by the session-load reconciliation) ---

  it('renders the interrupted status with an "— interrupted" hint', () => {
    render(makeSub({ status: 'interrupted' }))
    expect(container.textContent).toContain('interrupted')
    // A settled-looking row, not an auto-expanded body.
    expect(state()).toBe('closed')
  })

  it('renders a paused delegate with its distinct warning status icon', () => {
    render(makeSub({ status: 'paused' }))
    expect(container.querySelector('svg.text-warning')).not.toBeNull()
    // Paused is not interrupted: no stale "interrupted" hint.
    expect(container.textContent).not.toContain('interrupted')
  })

  // --- header cluster: checklist progress + context fill (parity with PlanStepBlock) ---

  const checklist = (checked: boolean[]): DisplayItem => ({
    kind: 'checklist',
    id: 'cl-1',
    stepId: 'del_1',
    active: true,
    items: checked.map(c => ({ text: c ? 'done thing' : 'todo thing', checked: c })),
  })

  it('shows the checklist progress (icon, green bar, N/M) before the context fill', () => {
    useChatStore.setState({ stepContextFill: { 'sess-1': { del_1: 42 } } })
    render(makeSub({ status: 'running', children: [CHILD, checklist([true, true, false])] }))
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
    // The bar is the success (green) tier regardless of progress.
    expect(checklistWrap!.querySelector('.bg-success')).not.toBeNull()
    expect(checklistWrap!.querySelector('.bg-info')).toBeNull()
  })

  it('renders the context fill without a checklist (no checklist cluster)', () => {
    useChatStore.setState({ stepContextFill: { 'sess-1': { del_1: 7 } } })
    render(makeSub({ status: 'running', children: [CHILD] }))
    const header = container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header.querySelector('[title^="Checklist:"]')).toBeNull()
    expect(header.querySelector('[title="Context fill: 7%"]')).not.toBeNull()
  })

  it('renders no header cluster without checklist or fill data', () => {
    render(makeSub({ status: 'running', children: [CHILD] }))
    const header = container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header.querySelector('[title^="Checklist:"]')).toBeNull()
    expect(header.querySelector('[title^="Context fill:"]')).toBeNull()
  })

  it('shows the token counts in the context tooltip (N of M)', () => {
    useChatStore.setState({
      stepContextFill: { 'sess-1': { del_1: 50 } },
      stepContextTokens: { 'sess-1': { del_1: { used_tokens: 4000, max_tokens: 8000 } } },
    })
    render(makeSub({ status: 'running', children: [CHILD] }))
    expect(container.querySelector('[title="Context fill: 4.0K of 8.0K"]')).not.toBeNull()
  })

  it('hides the context fill when tokens are spent but percent is missing', () => {
    useChatStore.setState({ stepContextFill: {}, stepContextTokens: { 'sess-1': { del_1: { used_tokens: 1, max_tokens: 10 } } } })
    render(makeSub({ status: 'running', children: [CHILD] }))
    expect(container.querySelector('[title^="Context fill:"]')).toBeNull()
  })

  it('re-renders the header when the checklist child updates', () => {
    render(makeSub({ status: 'running', children: [CHILD, checklist([true, false])] }))
    const header = () => container.querySelector('[data-slot="collapsible-trigger"]')!
    expect(header().querySelector('[title="Checklist: 1 of 2"]')).not.toBeNull()

    // Fresh wrapper objects (what groupMessages produces on every rebuild);
    // the recursive memo comparator must see the checked flip and re-render.
    const updated = makeSub({ status: 'running', children: [CHILD, checklist([true, true])] })
    act(() => {
      root.render(
        <BookmarkableContext.Provider value={false}>
          <SubAgentBlock item={updated} />
        </BookmarkableContext.Provider>,
      )
    })
    expect(header().querySelector('[title="Checklist: 1 of 2"]')).toBeNull()
    expect(header().querySelector('[title="Checklist: 2 of 2"]')).not.toBeNull()
  })

  // --- session-fill invariant: a delegate's step fill never touches the
  // session-level fill the status bar renders (enforced by handleContextFill;
  // asserted here against the store the block reads).

  it('leaves the session-level fill untouched by delegate step fills', () => {
    useChatStore.setState({
      sessionTokens: { 'sess-1': { fill_percent: 12, total_input_tokens: 100, total_output_tokens: 20, model: 'conductor', family: 'test' } },
      stepContextFill: { 'sess-1': { del_1: 55 } },
    })
    render(makeSub({ status: 'running', children: [CHILD] }))
    // The delegate header shows its own step fill...
    expect(container.querySelector('[title="Context fill: 55%"]')).not.toBeNull()
    // ...while the conductor's session-level fill stays exactly as it was.
    expect(useChatStore.getState().sessionTokens['sess-1']?.fill_percent).toBe(12)
  })
})
