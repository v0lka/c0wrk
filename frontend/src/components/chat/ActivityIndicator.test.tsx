// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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

  // Unmount before the next test's beforeEach store updates run: the rendered
  // tree stays subscribed to the stores, so setState outside act() on a live
  // component is what produces "not wrapped in act(...)" warnings.
  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (): void =>
    act(() => {
      root.render(<ActivityIndicator />)
    })

  it('renders nothing when there is no activity status and no running task', () => {
    render()
    expect(container.firstElementChild).toBeNull()
    expect(container.textContent).toBe('')
  })

  it('renders the activity status text', () => {
    useChatStore.setState({ activityStatus: { [SESSION]: 'Thinking...' } })
    render()
    expect(container.textContent).toContain('Thinking...')
  })

  it('renders only the blinking dot (no label text) while a task runs with no specific status yet', () => {
    // The ReAct loop briefly owns the slot with no label (e.g. right after a
    // strict-judge verdict clears it, before the next factual event lands).
    // Rather than collapsing the trailing block (which makes the whole chat jump
    // when the arriving label lands) the slot stays occupied by the blinking dot
    // alone — no label text is rendered for this idle gap.
    useChatStore.setState({ taskActive: { [SESSION]: true } })
    render()
    expect(container.textContent).toBe('')
    expect(container.firstElementChild).not.toBeNull()
    expect(container.querySelector('.animate-ping')).not.toBeNull()
  })

  it('prefers the specific status over the idle gap while a task runs', () => {
    useChatStore.setState({
      taskActive: { [SESSION]: true },
      activityStatus: { [SESSION]: 'Thinking...' },
    })
    render()
    expect(container.textContent).toBe('Thinking...')
  })

  it('collapses the indicator once the task ends (idle session)', () => {
    useChatStore.setState({ taskActive: { [SESSION]: true } })
    render()
    // While the task runs the dot holds the slot even without a status label.
    expect(container.firstElementChild).not.toBeNull()
    // task_complete: the task is no longer active, so even the dot goes away.
    act(() => {
      useChatStore.setState({ taskActive: {} })
    })
    render()
    expect(container.firstElementChild).toBeNull()
    expect(container.textContent).toBe('')
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
