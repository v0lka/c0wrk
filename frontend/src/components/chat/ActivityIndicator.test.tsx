// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ActivityIndicator } from './ActivityIndicator'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

const SESSION = 'sess-indicator'

describe('ActivityIndicator', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    useSessionStore.setState({ activeSessionId: SESSION })
    useChatStore.setState({ activityStatus: {}, pausing: {}, taskActive: {} })
  })

  const render = (): void =>
    act(() => {
      root.render(<ActivityIndicator />)
    })

  it('renders nothing when there is no activity status', () => {
    render()
    expect(container.textContent).toBe('')
  })

  it('renders the activity status text', () => {
    useChatStore.setState({ activityStatus: { [SESSION]: 'Thinking...' } })
    render()
    expect(container.textContent).toContain('Thinking...')
  })

  it('overrides any progress status with "Pausing" while a pause is in flight', () => {
    // The ReAct loop keeps emitting events until the step boundary lands —
    // step_start re-asserts "Thinking..." — but the label must stay pinned.
    useChatStore.setState({
      activityStatus: { [SESSION]: 'Thinking...' },
      pausing: { [SESSION]: true },
    })
    render()
    expect(container.textContent).toContain('Pausing')
    expect(container.textContent).not.toContain('Thinking...')
  })

  it('returns to the live status once the pause-in-flight flag clears', () => {
    useChatStore.setState({
      activityStatus: { [SESSION]: 'Thinking...' },
      pausing: { [SESSION]: true },
    })
    render()
    // session_paused / a failed pause request clears the flag.
    act(() => {
      useChatStore.setState({ pausing: {} })
    })
    render()
    expect(container.textContent).toContain('Thinking...')
  })
})
