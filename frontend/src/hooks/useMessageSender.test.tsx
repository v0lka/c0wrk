// Unit tests for useMessageSender — focused on the optimistic user message's
// metadata blob. The optimistic message must carry the goal flag, document
// attachment summaries, image records, and the nudge marker immediately (the
// same SNAKE_CASE shape the backend persists via PendingMessageMetadata), so
// the UserMessageMetaBadges indicators render on send instead of appearing
// only after a session/project switch reloads history from the DB.
//
// No @testing-library/react in this repo; we follow the established
// createRoot + jsdom harness pattern (see useStageAttachments.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useMessageSender, type UseMessageSenderResult } from '@/hooks/useMessageSender'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import type { SessionInfo, AttachmentInfoUI } from '@/types/models'
import type { ChatMessageUI } from '@/types/messages'

// Spies exist before vi.mock factories run so they can be referenced there.
const spies = vi.hoisted(() => ({
  sendMessage: vi.fn<(...args: unknown[]) => Promise<void>>(),
  createSession: vi.fn<() => Promise<SessionInfo>>(),
}))

vi.mock('@/api/chat', () => ({
  sendMessage: spies.sendMessage,
  cancelTask: vi.fn(),
}))

vi.mock('@/api/sessions', () => ({
  createSession: spies.createSession,
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

const makeSession = (id: string): SessionInfo => ({
  id,
  project_id: 'proj',
  name: 's',
  created_at: '',
  last_active_at: '',
  archived: false,
  pinned: false,
  active: true,
  total_input_tokens: 0,
  total_output_tokens: 0,
  model: '',
  family: '',
  has_unfinished_task: false,
  unfinished_task_status: '',
})

const DOC: AttachmentInfoUI = { id: 'd1', originalName: 'report.pdf', format: 'pdf', sizeBytes: 1024 }
const IMG: AttachmentInfoUI = {
  id: 'img-1',
  originalName: 'cat.png',
  format: 'png',
  sizeBytes: 2048,
  isImage: true,
  thumbnail: 'data:image/jpeg;base64,AAA',
  path: '/tmp/cat.png',
  mediaType: 'image/png',
}

// Harness: capture the send callback from the hook.
let capturedSend: UseMessageSenderResult['send'] | null = null
function Harness() {
  const { send } = useMessageSender()
  capturedSend = send
  return null
}

/** All user messages currently in the chat store for a session. */
function userMessages(sessionId: string): ChatMessageUI[] {
  const order = useChatStore.getState().messageOrder[sessionId] ?? []
  const index = useChatStore.getState().messages[sessionId] ?? {}
  return order
    .map((id) => index[id])
    .filter((m): m is ChatMessageUI => m !== undefined && m.type === 'user')
}

/** The single user message a test just sent; fails loudly when absent. */
function sentUser(sessionId: string): ChatMessageUI {
  const msgs = userMessages(sessionId)
  if (msgs.length === 0) throw new Error(`no user message in session ${sessionId}`)
  return msgs[msgs.length - 1]!
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  spies.sendMessage.mockReset().mockResolvedValue(undefined)
  spies.createSession.mockReset().mockResolvedValue(makeSession('fresh-session'))
  act(() => {
    useSessionStore.setState({ sessions: [], activeSessionId: null })
    useChatStore.setState({ messages: {}, messageOrder: {}, paused: {}, taskActive: {} })
    useInputModeStore.setState({ goalEnabled: false, goalBudget: '', selectedModel: null, selectedReasoning: null })
    useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
})

describe('useMessageSender optimistic metadata', () => {
  it('adds a user message with no metadata for a plain text send', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    await act(async () => {
      await capturedSend!('hello')
    })
    const msgs = userMessages('s1')
    expect(msgs).toHaveLength(1)
    expect(sentUser('s1').content).toBe('hello')
    expect(sentUser('s1').metadata).toBeUndefined()
  })

  it('pins the send to the ORIGIN session when the active session changed mid-flight', async () => {
    // Regression: send() used to re-read activeSessionId at call time —
    // after the controller's #agent catalog await the user may have switched
    // sessions, misrouting the message (and bypassing the origin's
    // uploads guard). The explicit originSessionId wins.
    useSessionStore.setState({ activeSessionId: 's-other' })
    await act(async () => {
      await capturedSend!('review this', undefined, [], 's-origin')
    })

    expect(spies.sendMessage).toHaveBeenCalledWith(
      's-origin', 'review this', [], [], '', '', false, '',
    )
    expect(userMessages('s-origin')).toHaveLength(1)
    expect(userMessages('s-other')).toHaveLength(0)
  })

  it('mirrors the goal flag into the optimistic message and resets the toggle', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useInputModeStore.setState({ goalEnabled: true, goalBudget: '{"max_turns":3}' })
    })
    await act(async () => {
      await capturedSend!('fix the bug')
    })
    expect(sentUser('s1').metadata).toEqual({ goal: true })
    // Goal is per-task opt-in: the toggle resets after the defining message.
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
    expect(useInputModeStore.getState().goalBudget).toBe('')
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'fix the bug', [], [], '', '', true, '{"max_turns":3}',
    )
  })

  it('mirrors document and image attachments into the optimistic message', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useAttachmentsStore.getState().setAttachments('s1', [DOC, IMG])
    })
    await act(async () => {
      await capturedSend!('summarize these')
    })
    expect(sentUser('s1').metadata).toEqual({
      attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 1024 }],
      images: [
        {
          id: 'img-1',
          name: 'cat.png',
          thumbnail: 'data:image/jpeg;base64,AAA',
          path: '/tmp/cat.png',
          media_type: 'image/png',
        },
      ],
    })
  })

  it('mirrors only the target session own pending attachments', async () => {
    // Per-session slices: s1 has a doc pending, s2 has an image. A send into
    // s2 must reflect s2's list only — never another session's.
    useSessionStore.setState({ activeSessionId: 's2' })
    act(() => {
      useAttachmentsStore.getState().setAttachments('s1', [DOC])
      useAttachmentsStore.getState().setAttachments('s2', [IMG])
    })
    await act(async () => {
      await capturedSend!('describe the picture')
    })
    expect(sentUser('s2').metadata).toEqual({
      images: [
        {
          id: 'img-1',
          name: 'cat.png',
          thumbnail: 'data:image/jpeg;base64,AAA',
          path: '/tmp/cat.png',
          media_type: 'image/png',
        },
      ],
    })
  })

  it('combines goal + attachments in one blob', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useInputModeStore.setState({ goalEnabled: true })
      useAttachmentsStore.getState().setAttachments('s1', [DOC])
    })
    await act(async () => {
      await capturedSend!('goal with doc')
    })
    expect(sentUser('s1').metadata).toEqual({
      goal: true,
      attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 1024 }],
    })
  })

  it('marks the optimistic message as a nudge when the session was paused', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useChatStore.setState({ paused: { s1: true } })
    })
    await act(async () => {
      await capturedSend!('keep going')
    })
    expect(sentUser('s1').metadata).toEqual({ is_nudge: true })
    // setPaused(false) deletes the key — absence encodes "not paused".
    expect(useChatStore.getState().paused.s1).toBeUndefined()
  })

  it('auto-creates a session when none is active and stages metadata there', async () => {
    act(() => {
      useInputModeStore.setState({ goalEnabled: true })
    })
    await act(async () => {
      await capturedSend!('first message')
    })
    expect(useSessionStore.getState().activeSessionId).toBe('fresh-session')
    expect(sentUser('fresh-session').metadata).toEqual({ goal: true })
  })
})
