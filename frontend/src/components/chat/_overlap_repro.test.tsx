// TEMPORARY repro harness — safe to delete.
// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatMessageUI } from '@/types/messages'
import type { SessionInfo } from '@/types/models'

vi.mock('@/api/chat', () => ({
  getSessionHistory: vi.fn().mockResolvedValue({ messages: [], next_cursor: '', has_more: false }),
  getSessionRuntimeStatus: vi.fn().mockResolvedValue(null),
  getPendingActions: vi.fn().mockResolvedValue(null),
  resolveStalePrompt: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), debug: vi.fn(), warn: vi.fn(), info: vi.fn() } }))
vi.mock('./ChatInput', () => ({ ChatInput: () => <div data-testid="chat-input-stub" /> }))
vi.mock('./ExecutionPanels', () => ({ ExecutionPanels: () => null }))
vi.mock('./BlackboardPanel', () => ({ BlackboardPanel: () => null }))
vi.mock('./BookmarksPanel', () => ({ BookmarksPanel: () => null }))

import { ChatArea } from './ChatArea'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

const S = 's1'
function msg(id: string, type: ChatMessageUI['type'], content: string, metadata: Record<string, unknown> = {}): ChatMessageUI {
  return { id, sessionId: S, type, content, metadata, timestamp: Date.now() }
}
function session(): SessionInfo {
  return {
    id: S, project_id: 'p1', name: 'S', created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z', archived: false, pinned: false, active: false,
    total_input_tokens: 0, total_output_tokens: 0, model: '', family: '',
    has_unfinished_task: false, unfinished_task_status: '',
  }
}
let root: Root | null = null
function render(): HTMLElement {
  const c = document.createElement('div')
  document.body.appendChild(c)
  root = createRoot(c)
  act(() => { root!.render(<ChatArea />) })
  return c
}
function countText(_c: HTMLElement, t: string): number {
  // Count across the WHOLE document (portals included).
  let n = 0
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  let node = walker.nextNode()
  while (node) { if ((node.textContent ?? '').includes(t)) n++; node = walker.nextNode() }
  return n
}
function setState(ids: string[], index: Record<string, ChatMessageUI>, extra: Record<string, unknown> = {}) {
  useChatStore.setState({
    messages: { [S]: index }, messageOrder: { [S]: ids }, taskActive: { [S]: true },
    activityStatus: {}, streamingText: {}, paused: {}, pausing: {}, workUnitStatus: {},
    ...extra,
  } as never)
}

describe('overlap repro', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    useSessionStore.setState({ sessions: [session()], activeSessionId: S } as never)
    setState(['u1', 'a1'], { u1: msg('u1', 'user', 'hello'), a1: msg('a1', 'assistant', 'world') })
  })
  afterEach(() => { act(() => { root?.unmount() }); root = null })

  it('transition idle -> live stays single', () => {
    const c = render()
    const seen: string[] = []
    const obs = new MutationObserver(() => { seen.push('mutation') })
    obs.observe(c, { childList: true, subtree: true, characterData: true })
    console.log('idle count', countText(c, 'Idle…'), 'live', countText(c, 'Thinking...'))
    act(() => { useChatStore.setState({ activityStatus: { [S]: 'Thinking...' } } as never) })
    obs.disconnect()
    console.log('after flip: Idle… count', countText(c, 'Idle…'), 'Thinking... count', countText(c, 'Thinking...'), 'mutations', seen.length)
    expect(countText(c, 'Idle…')).toBe(0)
    expect(countText(c, 'Thinking...')).toBe(1)
  })

  it('duplicate user message ids / trailing user turn', () => {
    setState(['u1', 'a1', 'u1'], { u1: msg('u1', 'user', 'hello'), a1: msg('a1', 'assistant', 'world') })
    const c = render()
    console.log('dup-id Idle… count', countText(c, 'Idle…'), 'ping', c.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })

  it('plan_step + subagent rows (nested renderers)', () => {
    setState(
      ['u1', 'ps1', 'psc1', 'sa1', 'sac1'],
      {
        u1: msg('u1', 'user', 'go'),
        ps1: msg('ps1', 'plan_step_start', '', { step_id: 'step_1', step_num: 1, title: 'Do', description: 'd' }),
        psc1: msg('psc1', 'plan_step_complete', '', { step_id: 'step_1', step_num: 1 }),
        sa1: msg('sa1', 'subagent_launch', '', { step_id: 'step_1', description: 'sub' }),
        sac1: msg('sac1', 'subagent_complete', '', { step_id: 'step_1' }),
      },
      { activityStatus: { [S]: 'Thinking...' } },
    )
    const c = render()
    console.log('plan/sub Idle… count', countText(c, 'Idle…'), 'Thinking... count', countText(c, 'Thinking...'), 'ping', c.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })

  it('session switch A(idle) -> B(live)', () => {
    const c = render()
    console.log('A: Idle… count', countText(c, 'Idle…'))
    act(() => {
      useSessionStore.setState({ activeSessionId: 's2' } as never)
      useChatStore.setState({
        messages: { s2: { u2: msg('u2', 'user', 'x') } },
        messageOrder: { s2: ['u2'] },
        taskActive: { s1: true, s2: true },
        activityStatus: { s2: 'Thinking...' },
      } as never)
    })
    console.log('B: Idle… count', countText(c, 'Idle…'), 'Thinking... count', countText(c, 'Thinking...'), 'ping', document.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })

  it('pausing while idle', () => {
    setState(['u1', 'a1'], { u1: msg('u1', 'user', 'hello'), a1: msg('a1', 'assistant', 'world') }, { pausing: { [S]: true } })
    const c = render()
    console.log('pausing: Idle… count', countText(c, 'Idle…'), 'Pausing count', countText(c, 'Pausing'), 'ping', document.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })

  it('empty messages + streaming only', () => {
    setState([], {}, { streamingText: { [S]: 'x' } })
    const c = render()
    console.log('stream-only: Idle… count', countText(c, 'Idle…'), 'ping', document.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })

  it('virtualized 70 rows + streaming', () => {
    const order: string[] = []
    const index: Record<string, ChatMessageUI> = {}
    for (let i = 0; i < 70; i++) { const id = `m${i}`; order.push(id); index[id] = msg(id, i % 2 === 0 ? 'user' : 'assistant', `m${i}`) }
    setState(order, index, { streamingText: { [S]: 'partial' }, activityStatus: { [S]: 'Thinking...' } })
    const c = render()
    console.log('virt Idle… count', countText(c, 'Idle…'), 'Thinking... count', countText(c, 'Thinking...'), 'ping', c.querySelectorAll('.animate-ping').length)
    expect(true).toBe(true)
  })
})
