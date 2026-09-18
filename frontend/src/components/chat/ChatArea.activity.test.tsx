// @vitest-environment jsdom
//
// Regression guard: ChatArea must render exactly ONE activity-indicator label
// across the whole document (portals included), and the label must flip
// atomically when the live activity status arrives — never rendering the
// "Idle…" placeholder and a concrete status ("Thinking...") at the same time.
//
// ActivityIndicator.test.tsx pins the label-selection logic at the component
// level; this file pins the ChatArea integration: a single indicator instance,
// mounted as the renderer's trailing content, whose label swaps in place when
// activityStatus lands.
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
function countText(t: string): number {
  // Count across the WHOLE document (portals included) so a duplicated
  // indicator instance anywhere would be caught.
  let n = 0
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  let node = walker.nextNode()
  while (node) { if ((node.textContent ?? '').includes(t)) n++; node = walker.nextNode() }
  return n
}

describe('ChatArea activity indicator overlap', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    useSessionStore.setState({ sessions: [session()], activeSessionId: S } as never)
    useChatStore.setState({
      messages: { [S]: { u1: msg('u1', 'user', 'hello'), a1: msg('a1', 'assistant', 'world') } },
      messageOrder: { [S]: ['u1', 'a1'] },
      taskActive: { [S]: true },
      activityStatus: {},
      streamingText: {},
      paused: {},
      pausing: {},
      workUnitStatus: {},
    } as never)
  })
  afterEach(() => { act(() => { root?.unmount() }); root = null })

  it('renders a single label that flips atomically from "Idle…" to "Thinking..."', () => {
    render()
    // While the task runs with no concrete status yet, exactly one "Idle…"
    // placeholder is shown and no concrete status leaked from elsewhere.
    expect(countText('Idle…')).toBe(1)
    expect(countText('Thinking...')).toBe(0)

    act(() => { useChatStore.setState({ activityStatus: { [S]: 'Thinking...' } } as never) })

    // The live status replaces the placeholder in place: the placeholder must
    // be gone and the status must appear exactly once.
    expect(countText('Idle…')).toBe(0)
    expect(countText('Thinking...')).toBe(1)
  })
})
