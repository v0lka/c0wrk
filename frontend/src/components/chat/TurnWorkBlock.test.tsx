// @vitest-environment jsdom
// Radix Presence needs requestAnimationFrame; jsdom lacks it. Also stubs
// ResizeObserver for the same reason (see the bookmarks tests).
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TurnWorkBlock } from './TurnWorkBlock'
import { turnWorkOwners } from './turnWorkOwners'
import { bookmarkKey } from '@/lib/bookmarks'
import { BookmarkableContext } from './BookmarkableContext'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'

let seq = 0
function mkMsg(type: ChatMessageUI['type'], content: string, timestamp = 0): ChatMessageUI {
  seq++
  return { id: `msg-${seq}`, sessionId: 's1', type, content, timestamp }
}

const assistant = (content = 'answer', timestamp?: number) =>
  ({ kind: 'assistant', message: mkMsg('assistant', content, timestamp) }) as DisplayItem
const tool = (toolName = 'bash') =>
  ({ kind: 'tool', id: `tool-${++seq}`, toolName, args: 'ls', status: 'success' }) as DisplayItem
const service = () =>
  ({ kind: 'service', id: `svc-${++seq}`, variant: 'status', content: 'Working…' }) as DisplayItem
const errorItem = (timestamp?: number) =>
  ({ kind: 'error', message: mkMsg('error', 'boom', timestamp) }) as DisplayItem
const memoryRead = (content = 'BODY_MARKER') =>
  ({ kind: 'memory_read', id: `mem-${++seq}`, content }) as DisplayItem

describe('TurnWorkBlock', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    })
    vi.stubGlobal('cancelAnimationFrame', () => {})
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    vi.unstubAllGlobals()
    container.remove()
    document.body.replaceChildren()
  })

  const render = (work: DisplayItem[], tail: DisplayItem[], tailSlot?: React.ReactNode, opts?: { live?: boolean }) =>
    act(() => {
      root.render(
        <BookmarkableContext.Provider value={false}>
          <TurnWorkBlock live={opts?.live ?? false} work={work} tail={tail} tailSlot={tailSlot} />
        </BookmarkableContext.Provider>,
      )
    })

  const state = () =>
    container.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')
  const trigger = () => container.querySelector('[data-slot="collapsible-trigger"]') as HTMLElement
  const click = () =>
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

  // --- auto open state (live-semantics) ---

  it('is open only while the turn is LIVE (empty tail + live=true)', () => {
    render([tool(), memoryRead()], [], undefined, { live: true })
    expect(state()).toBe('open')
    // Same empty tail but the turn is not live (paused / superseded / ended
    // without an answer): the block is settled — collapsed.
    render([tool(), memoryRead()], [], undefined, { live: false })
    expect(state()).toBe('closed')
  })

  it('auto-collapses when the turn stops being live, answer or not', () => {
    render([tool()], [], undefined, { live: true })
    expect(state()).toBe('open')
    // The answer commits while still live (an intermediate answer of a
    // multi-step task): the turn can still produce items — stays open.
    render([tool()], [assistant()], undefined, { live: true })
    expect(state()).toBe('open')
    // The task ends: settled regardless of the tail.
    render([tool()], [assistant()], undefined, { live: false })
    expect(state()).toBe('closed')
  })

  it('renders the body content while open and nothing once settled', () => {
    render([memoryRead()], [], undefined, { live: true })
    // The body renders inside the collapsible content slot (open).
    expect(container.querySelector('[data-slot="collapsible-content"]')!.textContent).toContain('BODY_MARKER')
    render([memoryRead()], [assistant()], undefined, { live: false })
    // Settled: collapsed — Radix keeps the (hidden) content wrapper but
    // unmounts the body; the marker remaining in the header preview is
    // expected.
    const content = container.querySelector('[data-slot="collapsible-content"]')
    expect(content?.getAttribute('data-state')).toBe('closed')
    expect(content?.textContent ?? '').not.toContain('BODY_MARKER')
  })

  // --- manual toggle vs the auto rule ---

  it('manual collapse wins over the auto-open state while still running', () => {
    render([tool()], [], undefined, { live: true })
    click()
    expect(state()).toBe('closed')
    // A re-render while still live (work grew) keeps the user's choice.
    render([tool(), tool()], [], undefined, { live: true })
    expect(state()).toBe('closed')
  })

  it('clears the manual override when the turn settles so the auto rule decides', () => {
    render([tool()], [], undefined, { live: true })
    // Collapse then re-expand: the override is now explicitly `true` while the
    // derived state is still open.
    click()
    click()
    expect(state()).toBe('open')
    // The turn settles (task ended) — the stale override must not keep the
    // block open.
    render([tool()], [], undefined, { live: false })
    expect(state()).toBe('closed')
  })

  it('keeps a manual expansion across unrelated re-renders after the commit', () => {
    render([tool()], [assistant()])
    expect(state()).toBe('closed')
    click()
    expect(state()).toBe('open')
    // New tail items (e.g. a trailing panel) do not fight the user's choice.
    render([tool()], [assistant(), memoryRead('PANEL_MARKER')])
    expect(state()).toBe('open')
  })

  // --- header: label, duration, preview, title ---

  it('renders "Work steps (N) · duration" from the first/last message timestamps', () => {
    render([assistant('intermediate', 1000), tool(), errorItem(3000)], [])
    expect(trigger().textContent).toContain('Work steps (3) · 2s')
  })

  it('omits the duration when timestamps are absent or inverted', () => {
    // Only timestamp-less items: no span computable.
    render([tool(), service(), memoryRead()], [])
    expect(trigger().textContent).toContain('Work steps (3)')
    expect(trigger().textContent).not.toContain('·')
    expect(trigger().getAttribute('title')).toBe('Work steps (3)')

    // Inverted span (end before start): omitted rather than negative.
    render([assistant('a', 5000), errorItem(1000)], [])
    expect(trigger().textContent).toContain('Work steps (2)')
    expect(trigger().textContent).not.toContain('·')

    // Sub-second span renders in ms.
    render([assistant('a', 1400), errorItem(1450)], [])
    expect(trigger().textContent).toContain('Work steps (2) · 50ms')
  })

  it('previews the last work item as "— <title>" with a matching title attribute', () => {
    render([tool(), memoryRead()], [])
    const preview = trigger().querySelector('span[title="BODY_MARKER"]')
    expect(preview).not.toBeNull()
    expect(preview!.textContent).toBe('— BODY_MARKER')
  })

  it('carries the label as the trigger title (string label)', () => {
    render([tool(), tool()], [assistant()])
    expect(trigger().getAttribute('title')).toBe('Work steps (2)')
  })

  it('renders an empty work block with no preview (interrupted marker only)', () => {
    render([], [])
    expect(trigger().textContent).toBe('Work steps (0)— interrupted')
    expect(trigger().getAttribute('title')).toBe('Work steps (0)')
    expect(trigger().querySelector('span[title]')).toBeNull()
  })

  // --- status icon ---

  it('shows a spinning info icon while the turn is live', () => {
    render([tool()], [], undefined, { live: true })
    const icon = container.querySelector('svg.text-info')
    expect(icon).not.toBeNull()
    expect(icon!.classList.contains('animate-spin')).toBe(true)
    // A committed intermediate answer does not change the live status.
    render([tool()], [assistant()], undefined, { live: true })
    expect(container.querySelector('svg.text-info')).not.toBeNull()
  })

  it('shows a success icon once the turn settled with an answer', () => {
    render([tool()], [assistant()], undefined, { live: false })
    expect(container.querySelector('svg.text-success')).not.toBeNull()
  })

  it('shows the interrupted marker/icon for a turn settled without an answer', () => {
    render([tool()], [], undefined, { live: false })
    expect(container.querySelector('svg.text-muted-foreground')).not.toBeNull()
    expect(trigger().textContent).toContain('— interrupted')
  })

  it('shows a failure icon when the work contains an error item (live or settled)', () => {
    render([tool(), errorItem()], [], undefined, { live: true })
    expect(container.querySelector('svg.text-destructive')).not.toBeNull()
    render([tool(), errorItem()], [assistant()], undefined, { live: false })
    expect(container.querySelector('svg.text-destructive')).not.toBeNull()
  })

  // --- body rendering and memo stability ---

  it('renders work items through ChatMessageRenderer when expanded', () => {
    const child = memoryRead('NESTED_BODY')
    render([tool('bash_exec'), child], [])
    expect(container.textContent).toContain('NESTED_BODY')
    expect(container.querySelector(`[data-chevron-reveal-id^="turn-work:"]`)).not.toBeNull()
  })

  it('re-renders the header when the work grows (fresh wrappers, structural memo)', () => {
    render([tool()], [])
    expect(trigger().textContent).toContain('Work steps (1)')
    // Fresh but structurally equal: no change demanded, header stays correct.
    render([tool()], [])
    expect(trigger().textContent).toContain('Work steps (1)')
    // A genuinely new item updates the count.
    render([tool(), memoryRead()], [])
    expect(trigger().textContent).toContain('Work steps (2)')
  })

  // --- tail render-slot ---

  it('renders the tailSlot inside the open block while the turn is live', () => {
    render([tool()], [], <div data-testid="stream">partial answer</div>, { live: true })
    const content = container.querySelector('[data-slot="collapsible-content"]')!
    expect(state()).toBe('open')
    expect(content.querySelector('[data-testid="stream"]')).not.toBeNull()
    // The slot renders BELOW the work list (inside the bordered body).
    const text = content.textContent ?? ''
    expect(text.indexOf('BODY')).toBeLessThan(text.indexOf('partial answer'))
  })

  it('drops the tailSlot once the turn settles (block collapsed)', () => {
    const slot = <div data-testid="stream">partial answer</div>
    render([tool()], [], slot, { live: true })
    expect(container.querySelector('[data-testid="stream"]')).not.toBeNull()
    render([tool()], [assistant('final')], slot, { live: false })
    expect(state()).toBe('closed')
    // Radix unmounts collapsed content: the stream is gone from the block.
    // (The settled answer itself is rendered OUTSIDE the block — by the root
    // renderer as a tail row — so it must NOT appear in the block's DOM.)
    expect(container.querySelector('[data-testid="stream"]')).toBeNull()
    expect(container.textContent).not.toContain('partial answer')
  })

  // --- header: checklist progress chip (plan-step parity) ---

  it('renders a checklist progress chip in the header, tinted by the block status accent', () => {
    const mkChecklist = (items: Array<{ text: string; checked: boolean }>): DisplayItem =>
      ({ kind: 'checklist', id: `cl-${++seq}`, stepId: null, items, active: true })
    // Running block: the chip follows the info accent.
    render([tool()], [mkChecklist([
      { text: 'a', checked: true },
      { text: 'b', checked: false },
    ])], undefined, { live: true })
    const chip = trigger().querySelector('[role="progressbar"]')
    expect(chip).not.toBeNull()
    expect(chip!.getAttribute('aria-valuenow')).toBe('1')
    expect(chip!.getAttribute('aria-valuemax')).toBe('2')
    expect(chip!.querySelector('svg.text-info')).not.toBeNull()
    // Settled with an answer: the chip follows the success accent.
    render([tool()], [mkChecklist([{ text: 'a', checked: true }]), assistant()], undefined, { live: false })
    const settledChip = trigger().querySelector('[role="progressbar"]')
    expect(settledChip).not.toBeNull()
    expect(settledChip!.querySelector('svg.text-success')).not.toBeNull()
  })

  it('shows the LAST checklist in the tree when several exist (they supersede each other)', () => {
    const mkChecklist = (items: Array<{ text: string; checked: boolean }>): DisplayItem =>
      ({ kind: 'checklist', id: `cl-${++seq}`, stepId: null, items, active: true })
    render(
      [mkChecklist([{ text: 'old-1', checked: true }, { text: 'old-2', checked: true }])],
      [mkChecklist([{ text: 'new-1', checked: true }, { text: 'new-2', checked: false }, { text: 'new-3', checked: false }])],
      undefined,
      { live: true },
    )
    const chip = trigger().querySelector('[role="progressbar"]')!
    expect(chip.getAttribute('aria-valuenow')).toBe('1')
    expect(chip.getAttribute('aria-valuemax')).toBe('3')
  })

  it('renders no checklist chip without a checklist', () => {
    render([tool()], [assistant()], undefined, { live: false })
    expect(trigger().querySelector('[role="progressbar"]')).toBeNull()
  })

  // --- header preview: in-flight checklist shows its last unchecked entry ---

  it('previews the last UNCHECKED entry of the current checklist (work or tail)', () => {
    const mkChecklist = (id: string, items: Array<{ text: string; checked: boolean }>): DisplayItem =>
      ({ kind: 'checklist', id, stepId: null, items, active: true })
    // Active checklists sink into the tail (grouping rule 7) — the preview
    // must find them there too.
    render(
      [tool()],
      [mkChecklist('cl-1', [
        { text: 'done step', checked: true },
        { text: 'current step marker', checked: false },
        { text: 'pending step', checked: false },
      ])],
      undefined,
      { live: true },
    )
    // The LAST unchecked entry, not the first.
    const preview = trigger().querySelector('span[title="pending step marker"]')
      ?? trigger().querySelector('span[title="current step marker"]')
      ?? trigger().querySelector('span[title="pending step"]')
    expect(preview).not.toBeNull()
    expect(preview!.textContent).toBe('— pending step')
  })

  it('falls back to the default preview once the checklist is fully checked', () => {
    const cl: DisplayItem = { kind: 'checklist', id: 'cl-1', stepId: null, items: [{ text: 'done step', checked: true }], active: false }
    render([tool()], [cl, assistant('final')], undefined, { live: false })
    const preview = trigger().querySelector('span[title="done step"]')
    expect(preview).toBeNull()
    // Default preview: the last WORK item (the tool card "bash"), not the
    // checklist, not the answer.
    expect(trigger().querySelector('span[title="bash"]')).not.toBeNull()
  })

  // --- memo reactivity on `live` ---

  it('re-renders on the live edge even when work/tail are structurally equal', () => {
    const work = [tool()]
    const tail: DisplayItem[] = [assistant('final')]
    render(work, tail, undefined, { live: true })
    expect(state()).toBe('open')
    expect(container.querySelector('svg.text-info')).not.toBeNull()
    // Only `live` flips (fresh task_active=false with the same item arrays).
    render(work, tail, undefined, { live: false })
    expect(state()).toBe('closed')
    expect(container.querySelector('svg.text-success')).not.toBeNull()
  })

  // --- turnWorkOwners registration ---

  it('registers every top-level work item under its revealId and unregisters on unmount', () => {
    const t1 = tool()
    const t2 = memoryRead('registered')
    render([t1, t2], [])
    const k1 = bookmarkKey(t1)
    const k2 = bookmarkKey(t2)
    const revealId = turnWorkOwners.get(k1)
    expect(revealId).toBeDefined()
    expect(turnWorkOwners.get(k2)).toBe(revealId) // both → same block

    // The revealId is stable and item-derived: matches the block's chevron id.
    expect(container.querySelector(`[data-chevron-reveal-id="${revealId}"]`)).not.toBeNull()

    act(() => { root.unmount() })
    expect(turnWorkOwners.get(k1)).toBeUndefined()
    expect(turnWorkOwners.get(k2)).toBeUndefined()
    root = createRoot(container) // afterEach unmounts again — tolerate that
  })

  it('re-registers when the block identity changes (different revealId owner)', () => {
    const t1 = tool()
    render([t1], [])
    const id1 = turnWorkOwners.get(bookmarkKey(t1))
    render([t1, memoryRead()], [])
    // Same first item → same revealId; both keys still owned by the block.
    expect(turnWorkOwners.get(bookmarkKey(t1))).toBe(id1)
    expect(id1).toBeDefined()
  })

  it('registers plan_step/subagent items under BOTH their event id and step id', () => {
    const planStep: DisplayItem = {
      kind: 'plan_step', id: 'plan-step-evt-1', stepId: 'step_7', stepNum: 1,
      title: 'Do it', status: 'completed', children: [],
    }
    const subagentItem: DisplayItem = {
      kind: 'subagent', id: 'sub-1', stepId: 'step_8',
      title: 'Agent', status: 'completed', children: [],
    }
    render([planStep, subagentItem], [])
    // Bookmarks navigate by the item's `id` (the plan_step_start EVENT id);
    // PlanView's scrollToStep navigates by the plan `stepId`. Both keys must
    // resolve to the same owning block.
    expect(turnWorkOwners.get('plan-step-evt-1')).toBeDefined()
    expect(turnWorkOwners.get('step_7')).toBe(turnWorkOwners.get('plan-step-evt-1'))
    expect(turnWorkOwners.get('sub-1')).toBeDefined()
    expect(turnWorkOwners.get('step_8')).toBe(turnWorkOwners.get('sub-1'))

    act(() => { root.unmount() })
    // Cleanup is symmetric: both key spaces are evicted.
    expect(turnWorkOwners.get('plan-step-evt-1')).toBeUndefined()
    expect(turnWorkOwners.get('step_7')).toBeUndefined()
    expect(turnWorkOwners.get('step_8')).toBeUndefined()
    root = createRoot(container) // afterEach unmounts again — tolerate that
  })
})
