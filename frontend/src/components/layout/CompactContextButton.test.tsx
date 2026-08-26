// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
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
    useChatStore.setState({ compacting: {} })
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
