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
import { useExperimentalStore } from '@/stores/experimentalStore'
import { useE2SStore } from '@/stores/e2sStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionWorkspaceStore } from '@/stores/sessionWorkspaceStore'
import type { SessionInfo, AttachmentInfoUI } from '@/types/models'
import type { ChatMessageUI } from '@/types/messages'

// Spies exist before vi.mock factories run so they can be referenced there.
const spies = vi.hoisted(() => ({
  sendMessage: vi.fn<(...args: unknown[]) => Promise<void>>(),
  createSession: vi.fn<() => Promise<SessionInfo>>(),
  listSessions: vi.fn<() => Promise<SessionInfo[]>>(),
}))

vi.mock('@/api/chat', () => ({
  sendMessage: spies.sendMessage,
  cancelTask: vi.fn(),
}))

vi.mock('@/api/sessions', () => ({
  createSession: spies.createSession,
  listSessions: spies.listSessions,
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
  spies.listSessions.mockReset().mockResolvedValue([])
  act(() => {
    useSessionStore.setState({ sessions: [], activeSessionId: null })
    useChatStore.setState({ messages: {}, messageOrder: {}, paused: {}, taskActive: {}, unfinishedTaskStatus: {} })
    useInputModeStore.setState({ goalEnabled: false, goalBudget: '', e2sEnabled: false, selectedModel: null, selectedReasoning: null })
    // Default the experimental E2S gate ON so E2S-specific tests exercise the
    // armed path; tests that cover the gate off override this explicitly.
    useExperimentalStore.setState({ enabled: true })
    useE2SStore.getState().clearAll()
    useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
    useSessionWorkspaceStore.setState({ choices: {}, consumingFor: null })
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
      await capturedSend!('review this', undefined, [], [], 's-origin')
    })

    // The last arg is the optimistic card's id — the backend echoes it back
    // in message_received so THIS surface dedups the echo by identity.
    // reviewMode stays explicitly false in position 11.
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's-origin', 'review this', [], [], [], '', '', false, '', false, false,
      userMessages('s-origin')[0]!.id,
      null,
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
      's1', 'fix the bug', [], [], [], '', '', true, '{"max_turns":3}', false, false,
      userMessages('s1')[0]!.id,
      null,
    )
  })

  it('stamps the goal badge for a /goal:N prefix send without the toggle', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    await act(async () => {
      await capturedSend!('/goal:5 ship it')
    })
    // The optimistic card carries the goal stamp though the TOGGLE flag went
    // out false — the backend arms goal mode from the prefix (badge parity;
    // lib/goalCommand mirrors the backend grammar).
    expect(sentUser('s1').metadata).toEqual({ goal: true })
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', '/goal:5 ship it', [], [], [], '', '', false, '', false, false,
      userMessages('s1')[0]!.id,
      null,
    )
    // Nothing to reset: the toggle was never armed.
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('suppresses the optimistic goal stamp when the leading plain /goal was partitioned as a mention', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    await act(async () => {
      // Shadowing configuration (ADR-106 D5): a real catalog entry named
      // goal owns the plain spelling — the controller threads it and the
      // backend mention strip consumes the token, so no goal arms and no
      // stamp persists. The optimistic mirror must agree (no badge a reload
      // would silently remove).
      await capturedSend!('/goal ship it', ['goal'], [], [])
    })
    expect(sentUser('s1').metadata).toBeUndefined()
  })

  it('still stamps the badge for a budgeted prefix even when goal threads as a mention elsewhere', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    await act(async () => {
      // A "/goal:5" token is never a mention token (the colon breaks the
      // plain-token boundary), so a qualified goal mention later in the text
      // cannot strip it — the activation stays honest and the badge fires.
      await capturedSend!('/goal:5 ship it /skill: goal', ['goal'], [], [])
    })
    expect(sentUser('s1').metadata).toEqual({ goal: true })
  })

  it('does not stamp goal metadata for an inert /goal:0 form', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    await act(async () => {
      await capturedSend!('/goal:0 ship it')
    })
    // An invalid budget form activates nothing — the card stays plain.
    expect(sentUser('s1').metadata).toBeUndefined()
  })

  it('mirrors the e2s flag into the send and resets the toggle (disarming goal)', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      // Arm goal first, then switch to E2S — the store's mutual exclusion
      // must leave E2S as the only armed mode before the send.
      useInputModeStore.setState({ goalEnabled: true })
      useInputModeStore.getState().setE2sEnabled(true)
    })
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
    await act(async () => {
      await capturedSend!('run with explicit state')
    })
    // e2s rides after goalBudget (position 10) — and goal must NOT be
    // re-armed by the send.
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'run with explicit state', [], [], [], '', '', false, '', true, false,
      userMessages('s1')[0]!.id,
      null,
    )
    // E2S is per-task opt-in: the toggle resets after the defining message.
    expect(useInputModeStore.getState().e2sEnabled).toBe(false)
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('suppresses the e2s flag when the effective gate is off (stale persisted toggle)', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useInputModeStore.getState().setE2sEnabled(true)
      // A persisted arming outlives the gate: experimental off must
      // neutralize it so the backend never sees a rejected send.
      useExperimentalStore.setState({ enabled: false })
    })
    await act(async () => {
      await capturedSend!('plain message')
    })
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'plain message', [], [], [], '', '', false, '', false, false,
      userMessages('s1')[0]!.id,
      null,
    )
  })

  it('drops a stale E2S snapshot when a fresh non-E2S task starts', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    // A previous E2S run left a snapshot for this session.
    useE2SStore.getState().applySnapshot('s1', { state: { objective: 'old' }, turn: 3 })
    expect(useE2SStore.getState().snapshots['s1']).toBeDefined()

    await act(async () => {
      await capturedSend!('follow-up in the default flow')
    })
    // The default-flow task supersedes the prior E2S run: the panel's snapshot
    // must not shadow the plan view.
    expect(useE2SStore.getState().snapshots['s1']).toBeUndefined()
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

describe('useMessageSender optimistic activation rollback', () => {
  it('restores the pre-send unfinished-task overlay when the send is rejected', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    act(() => {
      useChatStore.setState({ unfinishedTaskStatus: { s1: 'failed' } })
    })
    spies.sendMessage.mockRejectedValueOnce(new Error('rejected'))

    await act(async () => {
      await capturedSend!('hello')
    })

    // The optimistic activation pinned the overlay to ''; a rejected fresh send
    // must put the original value back so the still-unfinished session keeps its
    // colour and busy guard.
    expect(useChatStore.getState().unfinishedTaskStatus['s1']).toBe('failed')
    expect(useChatStore.getState().taskActive['s1']).toBe(false)
  })

  it('does not restore the overlay for a failed LIVE interjection', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    // Artificial pre-state (a running task never carries a failed overlay): it
    // exists only to prove the `wasPaused || !isRunning` guard skips the restore
    // for a live send — the rollback keeps the running state, so the overlay is
    // re-pinned to '' (a running session has no unfinished task).
    act(() => {
      useChatStore.setState({ taskActive: { s1: true }, unfinishedTaskStatus: { s1: 'failed' } })
    })
    spies.sendMessage.mockRejectedValueOnce(new Error('rejected'))

    await act(async () => {
      await capturedSend!('interject')
    })

    expect(useChatStore.getState().taskActive['s1']).toBe(true)
    expect(useChatStore.getState().unfinishedTaskStatus['s1']).toBe('')
  })
})

describe('useMessageSender consumption window (ADR-091 workspace bind)', () => {
  const BRANCH_CHOICE = { kind: 'branch' as const, branch: 'feat/x', createBranch: false, startPoint: '' }

  function armChoice(sessionId = 's1') {
    useSessionStore.setState({
      sessions: [makeSession(sessionId)],
      activeSessionId: sessionId,
    })
    useSessionWorkspaceStore.getState().setChoice(sessionId, BRANCH_CHOICE)
  }

  it('brackets the RPC with the consumption window and clears the choice on success', async () => {
    armChoice()
    // The backend FACT the row refresh must mirror (never a local guess).
    const boundFact = makeSession('s1')
    ;(boundFact as { workspace_binding?: SessionInfo['workspace_binding'] }).workspace_binding = {
      kind: 'managed_worktree',
      workspace_path: '/w/trees/s-1',
      worktree_name: 's-abc12345',
      branch: 'feat/x',
    }
    spies.listSessions.mockResolvedValue([boundFact])
    let settleSend: (() => void) | null = null
    spies.sendMessage.mockImplementationOnce(
      () => new Promise<void>((resolve) => { settleSend = resolve }),
    )

    let sendPromise: Promise<void> | null = null
    act(() => {
      sendPromise = capturedSend!('provision feat/x')
    })
    // The choice rides on the wire, and the window is OPEN while it flies.
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'provision feat/x', [], [], [], '', '', false, '', false, false,
      userMessages('s1')[0]!.id,
      { branch: 'feat/x', create_branch: false, start_point: '' },
    )
    expect(useSessionWorkspaceStore.getState().consumingFor).toBe('s1')
    // The pending choice survives the window (it IS the in-flight payload).
    expect(useSessionWorkspaceStore.getState().choices['s1']).toBeDefined()

    await act(async () => {
      settleSend!()
      await sendPromise
    })

    // The window settled and the consumed choice is gone.
    expect(useSessionWorkspaceStore.getState().consumingFor).toBeNull()
    expect(useSessionWorkspaceStore.getState().choices['s1']).toBeUndefined()
    // The session row mirrors the backend FACT (the refreshed binding), not
    // an optimistic managed guess.
    const row = useSessionStore.getState().sessions?.find((s) => s.id === 's1')
    expect(row?.workspace_binding).toEqual(boundFact.workspace_binding)
  })

  it('re-arms the choice and refreshes the row when the send is rejected', async () => {
    armChoice()
    spies.sendMessage.mockRejectedValueOnce(new Error('branch busy'))
    // The row refresh runs on the failure path too: a failure AFTER a
    // successful bind leaves the binding persisted — the row must say so.
    const boundFact = makeSession('s1')
    ;(boundFact as { workspace_binding?: SessionInfo['workspace_binding'] }).workspace_binding = {
      kind: 'managed_worktree',
      workspace_path: '/w/trees/s-1',
      worktree_name: 's-abc12345',
      branch: 'feat/x',
    }
    spies.listSessions.mockResolvedValue([boundFact])

    await act(async () => {
      await capturedSend!('provision feat/x')
    })

    expect(useSessionWorkspaceStore.getState().consumingFor).toBeNull()
    // The bind was refused before any backend state changed: the choice is
    // re-armed so a corrected resend carries it again.
    expect(useSessionWorkspaceStore.getState().choices['s1']).toEqual(BRANCH_CHOICE)
    // The optimistic user message is rolled back; the failure card remains.
    expect(userMessages('s1')).toHaveLength(0)
    // The fact refresh mirrored the persisted binding into the row.
    const row = useSessionStore.getState().sessions?.find((s) => s.id === 's1')
    expect(row?.workspace_binding).toEqual(boundFact.workspace_binding)
  })

  it('does NOT re-arm the choice for a session deleted mid-provisioning', async () => {
    armChoice()
    // The user deletes the session while the send (provisioning) is in
    // flight: the delete path drops the choice slice and removes the row.
    spies.sendMessage.mockImplementationOnce(async () => {
      useSessionWorkspaceStore.getState().dropSessions(['s1'])
      useSessionStore.setState({ sessions: [], activeSessionId: null })
      throw new Error('bind refused after deletion')
    })

    await act(async () => {
      await capturedSend!('provision feat/x')
    })

    expect(useSessionWorkspaceStore.getState().consumingFor).toBeNull()
    // The choice was already dropped by the delete path; the failed send's
    // teardown must not resurrect it onto the dead id.
    expect(useSessionWorkspaceStore.getState().choices['s1']).toBeUndefined()
  })

  it('never opens the window for a null workspace choice', async () => {
    useSessionStore.setState({ sessions: [makeSession('s1')], activeSessionId: 's1' })
    await act(async () => {
      await capturedSend!('plain local send')
    })
    expect(useSessionWorkspaceStore.getState().consumingFor).toBeNull()
    expect(spies.listSessions).not.toHaveBeenCalled()
    expect(spies.sendMessage).toHaveBeenCalledWith(
      's1', 'plain local send', [], [], [], '', '', false, '', false, false,
      userMessages('s1')[0]!.id, null,
    )
  })

  it('refuses a workspace-carrying send while another consumption window is open', async () => {
    armChoice()
    act(() => {
      // Another send's window is already open (the backend is provisioning).
      useSessionWorkspaceStore.getState().beginConsumption('s1')
    })

    await act(async () => {
      // The refusal REJECTS: the caller's catch (useChatInputController)
      // restores the typed text and surfaces the reason via the sendError
      // hint — a normal resolve would strand the already-cleared editor.
      await expect(capturedSend!('second enter mid-provisioning')).rejects.toThrow(
        /already being prepared/,
      )
    })

    // The refusal: no RPC, no optimistic residue.
    expect(spies.sendMessage).not.toHaveBeenCalled()
    expect(userMessages('s1')).toHaveLength(0)
    // The busy window's own state is untouched.
    expect(useSessionWorkspaceStore.getState().consumingFor).toBe('s1')
  })

  it('backstop refusal throws before any optimistic state and leaves no transcript card', async () => {
    armChoice()
    // A racing send took the window between the early guard and the take:
    // beginConsumption refuses. No await sits between the guard and the
    // take today, so the backstop cannot be reached through interleaving —
    // the mock pins its contract against future refactors (any interposed
    // await must keep this path text-preserving and card-free).
    const original = useSessionWorkspaceStore.getState().beginConsumption
    useSessionWorkspaceStore.setState({ beginConsumption: () => false })

    try {
      await act(async () => {
        // The refusal REJECTS: it propagates to the caller (whose catch
        // restores the typed text) instead of being swallowed by the send's
        // own catch into a phantom transcript error card.
        await expect(capturedSend!('second enter mid-provisioning')).rejects.toThrow(
          /already being prepared/,
        )
      })
    } finally {
      useSessionWorkspaceStore.setState({ beginConsumption: original })
    }

    // No optimistic residue: no user card, no transcript error card, no RPC.
    expect(spies.sendMessage).not.toHaveBeenCalled()
    expect(userMessages('s1')).toHaveLength(0)
    const cards = Object.values(useChatStore.getState().messages['s1'] ?? {})
    expect(cards.filter((m) => m.type === 'error')).toHaveLength(0)
    // The choice was never consumed and the window did not leak.
    expect(useSessionWorkspaceStore.getState().choices['s1']).toEqual(BRANCH_CHOICE)
    expect(useSessionWorkspaceStore.getState().consumingFor).toBeNull()
  })
})
