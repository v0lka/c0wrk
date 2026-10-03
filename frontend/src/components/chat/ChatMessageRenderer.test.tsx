// @vitest-environment jsdom
// Radix Presence needs requestAnimationFrame (jsdom lacks it); ResizeObserver
// likewise — both are required once TurnWorkBlock mounts in the sticky path.
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { turnWorkOwners } from './turnWorkOwners'
import { collapsibleRegistry } from './collapsibleRegistry'

let root: Root | null = null

function message(id: string, type: ChatMessageUI['type'], content: string): ChatMessageUI {
  return {
    id,
    sessionId: 'session-1',
    type,
    content,
    metadata: {},
    timestamp: 0,
  }
}

const assistant = (id: string, content: string): DisplayItem =>
  ({ kind: 'assistant', message: message(id, 'assistant', content) })
const tool = (id: string, name = 'bash'): DisplayItem =>
  ({ kind: 'tool', id, toolName: name, args: 'ls', status: 'success' })
const thought = (id: string, content: string): DisplayItem =>
  ({ kind: 'thought', id, stepNum: 1, content })

// Turn 1 is a settled conversation turn; turn 2 carries work between the
// user message and its answer.
const items: DisplayItem[] = [
  { kind: 'user', message: message('user-1', 'user', 'First question') },
  assistant('assistant-1', 'First answer'),
  { kind: 'user', message: message('user-2', 'user', 'Second question') },
  thought('th-1', 'thinking'),
  tool('tool-1'),
  assistant('assistant-2', 'Second answer'),
]

function renderRenderer(overrides?: {
  items?: DisplayItem[]
  trailingContent?: React.ReactNode
  trailingFooter?: React.ReactNode
  lastTurnActive?: boolean
  bookmarkable?: boolean
}): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(
      <ChatMessageRenderer
        items={overrides?.items ?? items}
        stickyUserMessages
        lastTurnActive={overrides?.lastTurnActive ?? false}
        trailingContent={overrides?.trailingContent ?? <div data-testid="trailing">Streaming</div>}
        trailingFooter={overrides?.trailingFooter}
        bookmarkable={overrides?.bookmarkable}
      />,
    )
  })
  return container
}

function turnRoots(container: HTMLElement): HTMLElement[] {
  return Array.from(container.children) as HTMLElement[]
}

describe('ChatMessageRenderer sticky user turns', () => {
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
    document.body.replaceChildren()
  })

  afterEach(() => {
    act(() => root?.unmount())
    vi.unstubAllGlobals()
    root = null
  })

  it('groups each user message with content up to the next user message', () => {
    const container = renderRenderer()
    const turns = turnRoots(container)

    expect(turns).toHaveLength(2)
    expect(turns[0]?.querySelector('[data-message-id="user-1"]')).not.toBeNull()
    expect(turns[0]?.textContent).toContain('First answer')
    expect(turns[0]?.querySelector('[data-message-id="user-2"]')).toBeNull()
    expect(turns[1]?.querySelector('[data-message-id="user-2"]')).not.toBeNull()
    expect(turns[1]?.textContent).toContain('Second answer')
  })

  it('renders every history user exactly once with the sticky DOM contract', () => {
    const container = renderRenderer()

    for (const id of ['user-1', 'user-2']) {
      const matches = container.querySelectorAll(`[data-message-id="${id}"]`)

      expect(matches).toHaveLength(1)
      expect(matches[0]?.classList.contains('sticky')).toBe(true)
      expect(matches[0]?.classList.contains('top-0')).toBe(true)
    }

    expect(container.querySelectorAll('[data-message-id^="user-"]')).toHaveLength(2)
  })

  it('routes trailing activity through the last turn only (slot inside its block)', () => {
    const container = renderRenderer()
    const turns = turnRoots(container)

    expect(turns).toHaveLength(2)
    // No earlier turn ever carries trailing content.
    expect(turns[0]?.querySelector('[data-testid="trailing"]')).toBeNull()
    // Turn 2 is settled (task not active): its work block is collapsed, so the
    // slot is unmounted with the Radix content — the stream/indicator only
    // occupy DOM while the turn is live (covered by the open-block tests).
    expect(turns[1]?.querySelector('[data-testid="trailing"]')).toBeNull()
    // While the SAME turn runs (lastTurnActive), the slot lives inside the
    // open block.
    const live = renderRenderer({
      items: items.slice(2, 5), // user-2, thought, tool — no answer yet
      lastTurnActive: true,
    })
    const liveTurn = turnRoots(live)[0]!
    expect(liveTurn.querySelector('[data-testid="trailing"]')).not.toBeNull()
    expect(
      liveTurn.querySelector('[data-chevron-reveal-id^="turn-work:"]')!
        .contains(liveTurn.querySelector('[data-testid="trailing"]')!),
    ).toBe(true)
  })

  // --- turn work blocks (segmented turns) ---

  it('wraps a settled turn with work into a collapsed TurnWorkBlock and tail rows stay outside', () => {
    const container = renderRenderer()
    const turns = turnRoots(container)

    // Turn 1 (no work) renders no work block.
    expect(turns[0]?.querySelector('[data-chevron-reveal-id^="turn-work:"]')).toBeNull()

    // Turn 2 (thought + tool between user and answer) renders exactly one.
    const block = turns[1]?.querySelector('[data-chevron-reveal-id^="turn-work:"]')
    expect(block).not.toBeNull()
    // Committed history → collapsed.
    expect(block?.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')).toBe('closed')
    // Work content is unmounted (collapsed Radix content), answer outside.
    expect(turns[1]?.textContent).toContain('Second answer')
    expect(turns[1]?.textContent).not.toContain('thinking')
  })

  it('keeps the live turn OPEN with the stream inside the block via the render-slot', () => {
    // Live shape: user → thought → tool, no answer yet.
    const live: DisplayItem[] = [
      { kind: 'user', message: message('user-1', 'user', 'Do it') },
      thought('th-1', 'thinking'),
      tool('tool-1'),
    ]
    const container = renderRenderer({
      items: live,
      lastTurnActive: true,
      trailingContent: <div data-testid="stream">partial</div>,
    })
    const turn = turnRoots(container)[0]!
    const block = turn.querySelector('[data-chevron-reveal-id^="turn-work:"]')!
    const content = block.querySelector('[data-slot="collapsible-content"]')!

    expect(content.getAttribute('data-state')).toBe('open')
    // Work AND the stream are inside the open block.
    const text = content.textContent ?? ''
    expect(text).toContain('thinking')
    const stream = content.querySelector('[data-testid="stream"]')
    expect(stream).not.toBeNull()
    // Work renders above the stream (slot sits below the work list).
    expect(text.indexOf('thinking')).toBeLessThan(text.indexOf('partial'))
  })

  it('renders an unresolved confirmation panel OUTSIDE the work block while the run waits on it', () => {
    // The wait-for-confirmation shape: no answer yet, the panel is the only
    // tail item. It must sit outside the collapsible (Radix unmounts collapsed
    // content — a buried panel would be unreachable while the user must act).
    const live: DisplayItem[] = [
      { kind: 'user', message: message('user-1', 'user', 'Do it') },
      tool('tool-1'),
      {
        kind: 'tool_confirm',
        message: message('tool-confirm-1', 'tool_confirm', 'Confirm: bash_exec'),
      },
    ]
    const container = renderRenderer({ items: live, lastTurnActive: true })
    const turn = turnRoots(container)[0]!
    const block = turn.querySelector('[data-chevron-reveal-id^="turn-work:"]')!
    const panel = turn.querySelector('[data-bookmark-id="tool-confirm-1"]')

    expect(panel).not.toBeNull()
    // Outside the block entirely — a sibling of the collapsible, not its child.
    expect(block.contains(panel!)).toBe(false)
    // The tool card (plain work) stays inside the block.
    expect(
      block.querySelector('[data-slot="collapsible-content"]')!.contains(
        turn.querySelector('[data-bookmark-id="tool-1"]')!,
      ),
    ).toBe(true)
  })

  it('commits the live turn in place: stream swaps from slot to outside rows when the task ends', () => {
    const live: DisplayItem[] = [
      { kind: 'user', message: message('user-1', 'user', 'Do it') },
      thought('th-1', 'thinking'),
      tool('tool-1'),
    ]
    const container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)

    const renderWith = (its: DisplayItem[], taskActive: boolean) =>
      act(() => {
        root!.render(
          <ChatMessageRenderer
            items={its}
            stickyUserMessages
            lastTurnActive={taskActive}
            trailingContent={<div data-testid="stream">partial</div>}
          />,
        )
      })

    renderWith(live, true)
    let block = turnRoots(container)[0]!.querySelector('[data-chevron-reveal-id^="turn-work:"]')!
    expect(block.querySelector('[data-slot="collapsible-content"]')!.getAttribute('data-state')).toBe('open')
    expect(container.querySelector('[data-testid="stream"]')).not.toBeNull()

    // The task ends (same renderer instance, same turn container).
    renderWith([...live, assistant('assistant-1', 'Done')], false)
    const turn = turnRoots(container)[0]!
    block = turn.querySelector('[data-chevron-reveal-id^="turn-work:"]')!
    expect(block.querySelector('[data-slot="collapsible-content"]')!.getAttribute('data-state')).toBe('closed')
    // Stream slot is gone; the settled answer is a plain row outside the block.
    expect(container.querySelector('[data-testid="stream"]')).toBeNull()
    expect(turn.textContent).toContain('Done')
    expect(turn.textContent).not.toContain('thinking')
  })

  it('renders a nudge-superseded / dead turn as a COLLAPSED interrupted block (not flat)', () => {
    const dead: DisplayItem[] = [
      { kind: 'user', message: message('user-0', 'user', 'abandoned') },
      thought('th-0', 'lost work'),
      { kind: 'user', message: message('user-1', 'user', 'next') },
      assistant('assistant-1', 'ok'),
    ]
    const container = renderRenderer({ items: dead })
    const turns = turnRoots(container)

    // Turn 0: no answer ever arrived → collapsed interrupted block, not flat.
    const deadBlock = turns[0]?.querySelector('[data-chevron-reveal-id^="turn-work:"]')
    expect(deadBlock).not.toBeNull()
    expect(deadBlock?.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')).toBe('closed')
    // Its work items are unmounted (collapsed): no DOM anchor, owner registry
    // holds it; "lost work" surfaces only inside the header preview.
    expect(turns[0]?.querySelector('[data-bookmark-id="th-0"]')).toBeNull()
    expect(turns[0]?.textContent).toContain('— interrupted')
    expect(turnWorkOwners.get('th-0')).toBeDefined()
    // Turn 1 is the last, but has no work → flat too.
    expect(turns[1]?.querySelector('[data-chevron-reveal-id^="turn-work:"]')).toBeNull()
  })

  it('exposes no duplicate DOM anchors: work items render exactly once (inside the block)', () => {
    const container = renderRenderer()
    // The thought and the tool belong to turn 2's work and live only inside
    // the block. Their BookmarkableRow anchors exist only when the block is
    // open; collapsed, the rows are unmounted but registered in the owners
    // registry.
    expect(container.querySelectorAll('[data-bookmark-id="th-1"]')).toHaveLength(0)
    expect(container.querySelectorAll('[data-bookmark-id="tool-1"]')).toHaveLength(0)
    expect(turnWorkOwners.get('th-1')).toBeDefined()
    expect(turnWorkOwners.get('tool-1')).toBeDefined()
    // Same owner for both work items of the turn.
    expect(turnWorkOwners.get('th-1')).toBe(turnWorkOwners.get('tool-1'))
    // The tail answer keeps its own DOM anchor.
    expect(container.querySelectorAll('[data-bookmark-id="assistant-2"]').length).toBe(1)
  })

  it('registers collapsed-work anchors so navigation can reveal them (integration shape)', () => {
    const container = renderRenderer()
    const revealId = turnWorkOwners.get('tool-1')!
    // The registry handed the nav a live owner: expanding it mounts the work
    // content and its DOM anchors.
    const expand = collapsibleRegistry.get(revealId)
    expect(expand).toBeTypeOf('function')

    act(() => { expand!(true) })
    const content = container
      .querySelector(`[data-chevron-reveal-id="${revealId}"]`)!
      .querySelector('[data-slot="collapsible-content"]')!
    expect(content.getAttribute('data-state')).toBe('open')
    expect(container.querySelectorAll('[data-bookmark-id="tool-1"]').length).toBe(1)
    expect(container.querySelectorAll('[data-bookmark-id="th-1"]').length).toBe(1)
  })
})