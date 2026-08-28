// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CompactContextButton } from './CompactContextButton'
import { COMPACTION_STRATEGIES } from './compactionStrategies'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'

describe('CompactContextButton', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    useSessionStore.setState({ activeSessionId: 'sess-1' })
    useChatStore.setState({ compacting: {}, compactionNoOp: {} })
  })

  // Unmount before the next test's beforeEach store updates run: the rendered
  // tree (incl. Radix tooltip internals) stays subscribed to the stores, so
  // setState outside act() on a live component is what produces "not wrapped
  // in act(...)" warnings.
  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = () =>
    act(() => {
      root.render(<CompactContextButton />)
    })

  it('renders the compact trigger with the "Compact context" tooltip when idle', () => {
    render()
    const btn = container.querySelector('button[title="Compact context"]')
    expect(btn).not.toBeNull()
    expect(btn!.getAttribute('aria-label')).toBe('Compact context')
    // No cancel affordance while idle.
    expect(container.querySelector('button[title="Cancel compaction"]')).toBeNull()
  })

  it('swaps to the cancel button while compacting', () => {
    useChatStore.setState({ compacting: { 'sess-1': true } })
    render()
    const cancel = container.querySelector('button[title="Cancel compaction"]')
    expect(cancel).not.toBeNull()
    expect(cancel!.getAttribute('aria-label')).toBe('Cancel compaction')
    // The compact trigger (dropdown) is gone.
    expect(container.querySelector('button[title="Compact context"]')).toBeNull()
  })

  it('renders disabled with the under-target tooltip when a no-op is predicted', () => {
    // The backend predicts a manual compaction cannot shrink the dialogue —
    // the strategy menu is replaced by a disabled button with the reason.
    useChatStore.setState({ compactionNoOp: { 'sess-1': true } })
    render()
    const btn = container.querySelector<HTMLButtonElement>(
      'button[title="Context is already under the compaction target"]',
    )
    expect(btn).not.toBeNull()
    expect(btn!.disabled).toBe(true)
    // The dropdown trigger is gone.
    expect(container.querySelector('button[title="Compact context"]')).toBeNull()
    expect(container.querySelector('button[title="Cancel compaction"]')).toBeNull()
  })

  it('fails open when the no-op verdict is unknown (absent key)', () => {
    // Undefined verdict (older backend / not yet fetched) must NOT disable
    // the button — the normal strategy menu stays available.
    useChatStore.setState({ compactionNoOp: {} })
    render()
    expect(container.querySelector('button[title="Compact context"]')).not.toBeNull()
    expect(
      container.querySelector('button[title="Context is already under the compaction target"]'),
    ).toBeNull()
  })

  it('keeps the cancel affordance while compacting even when a no-op is predicted', () => {
    // A compaction is in flight — the cancel affordance wins over the
    // disabled no-op state (the flow must remain interruptable).
    useChatStore.setState({ compacting: { 'sess-1': true }, compactionNoOp: { 'sess-1': true } })
    render()
    expect(container.querySelector('button[title="Cancel compaction"]')).not.toBeNull()
    expect(
      container.querySelector('button[title="Context is already under the compaction target"]'),
    ).toBeNull()
  })

  it('renders nothing interactive missing — hidden session keeps idle state', () => {
    useSessionStore.setState({ activeSessionId: null })
    render()
    // No active session: compacting flag cannot be set, so the idle trigger
    // renders; the click handler is a no-op (guarded on activeSessionId).
    expect(container.querySelector('button[title="Compact context"]')).not.toBeNull()
  })

  it('exposes the three documented strategies, each with an explanatory tooltip', () => {
    expect(COMPACTION_STRATEGIES.map((s) => s.id)).toEqual([
      'sliding_window',
      'summarization',
      'hierarchical',
    ])
    for (const s of COMPACTION_STRATEGIES) {
      expect(s.name.length).toBeGreaterThan(0)
      expect(s.tooltip.length).toBeGreaterThan(40) // explains when to use it
      expect(s.tooltip).toMatch(/[Bb]est for/)
    }
  })
})
