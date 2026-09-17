// TEMPORARY probe — safe to delete.
// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ActivityIndicator } from './ActivityIndicator'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

const S = 's1'

describe('ActivityIndicator node reconciliation', () => {
  let container: HTMLElement
  let root: Root
  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    useSessionStore.setState({ activeSessionId: S } as never)
    useChatStore.setState({ activityStatus: {}, pausing: {}, taskActive: {} } as never)
  })
  afterEach(() => { act(() => { root.unmount() }); document.body.replaceChildren() })

  it('reports node identity across idle -> live', () => {
    // idle state
    act(() => { useChatStore.setState({ taskActive: { [S]: true } } as never) })
    act(() => { root.render(<ActivityIndicator />) })
    const dotIdle = container.querySelector('span.bg-muted-foreground\\/40') as HTMLElement
    console.log('idle dot found:', !!dotIdle, 'classes:', dotIdle?.className)
    const row = container.firstElementChild as HTMLElement
    console.log('row children (idle):', Array.from(row.children).map((el) => el.className).join(' | '))
    const dotWrapChildrenIdle = Array.from((row.children[0] as HTMLElement).children)

    // flip to live
    act(() => { useChatStore.setState({ activityStatus: { [S]: 'Thinking...' } } as never) })
    const row2 = container.firstElementChild as HTMLElement
    console.log('row children (live):', Array.from(row2.children).map((el) => el.className).join(' | '))
    const dotWrapChildrenLive = Array.from((row2.children[0] as HTMLElement).children)
    console.log('dot wrapper children (idle):', dotWrapChildrenIdle.map((el) => el.className))
    console.log('dot wrapper children (live):', dotWrapChildrenLive.map((el) => el.className))
    console.log('idle dot node reused as ping?', dotWrapChildrenLive[0] === dotIdle)
    expect(true).toBe(true)
  })
})
