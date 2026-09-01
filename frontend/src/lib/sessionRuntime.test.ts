import { describe, it, expect, beforeEach, vi } from 'vitest'
import { reconcileRuntimeStatus, reconcilePendingActions, refreshCompactionNoOp, stalePromptMatchField } from './sessionRuntime'
import { handleSessionPausedEvent, handleSessionResumedEvent } from '@/hooks/events/sessionLifecycleHandlers'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ChatMessageUI } from '@/types/messages'
import { getSessionRuntimeStatus } from '@/api/chat'
import type { PendingActionsResponse } from '@/api/chat'

// refreshCompactionNoOp calls the runtime-status RPC — stub it (the module
// also re-exports window-touching wails wrappers, so a factory mock keeps the
// node-env test free of the real module).
vi.mock('@/api/chat', () => ({
  getSessionRuntimeStatus: vi.fn(),
}))

const SESSION = 'sess-1'

function resetStore(): void {
  useChatStore.setState({
    messages: {},
    messageOrder: {},
    taskActive: {},
    streamingText: {},
    activityStatus: {},
    paused: {},
    pausing: {},
    compacting: {},
    compactionNoOp: {},
    runtimeEventAt: {},
    taskFlagsEventAt: {},
  })
  // reconcileRuntimeStatus mirrors has_unfinished_task into the session list;
  // reset it so tests start from a clean (null) snapshot state.
  useSessionStore.setState({ sessions: null, activeSessionId: null })
}

function addMsg(overrides: Partial<ChatMessageUI> & { id: string; type: ChatMessageUI['type'] }): void {
  useChatStore.getState().addMessage(SESSION, {
    sessionId: SESSION,
    content: '',
    timestamp: Date.now(),
    ...overrides,
  })
}

function sessionMessages(): ChatMessageUI[] {
  const s = useChatStore.getState()
  return (s.messageOrder[SESSION] ?? []).map(id => s.messages[SESSION]![id]!) 
}

describe('reconcileRuntimeStatus', () => {
  beforeEach(resetStore)

  it('mirrors the compaction_noop prediction into the per-session store flag', () => {
    // The backend predicts a manual compaction would change nothing — the
    // compact button must render disabled for this session after the
    // reconcile (session switch/restart).
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false, compaction_noop: true })
    expect(useChatStore.getState().compactionNoOp[SESSION]).toBe(true)
  })

  it('clears a stale no-op flag when the backend reports compactable again', () => {
    // A finished task grew the history past the target while unobserved; the
    // fresh snapshot reports compaction_noop=false and the reconcile must
    // lift a previously-disabled button.
    useChatStore.setState({ compactionNoOp: { [SESSION]: true } })
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false, compaction_noop: false })
    expect(useChatStore.getState().compactionNoOp[SESSION]).toBeUndefined()
  })

  it('treats an absent compaction_noop field as fail-open (flag cleared)', () => {
    // Older backend without the field must not leave a stale disabled state.
    useChatStore.setState({ compactionNoOp: { [SESSION]: true } })
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })
    expect(useChatStore.getState().compactionNoOp[SESSION]).toBeUndefined()
  })

  it('replaces a frozen activity label with the backend-tracked phase for an active session', () => {    // Reproduces the "Routing request..." stuck bug: the label froze when the
    // user switched away mid-routing, while the session advanced far past it.
    useChatStore.setState({
      activityStatus: { [SESSION]: 'Routing request...' },
      taskActive: { [SESSION]: false },
    })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: false, activity: 'Thinking...' })

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Thinking...')
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
  })

  it('falls back to the generic label for an active session without tracked activity', () => {
    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: false, paused: false })

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Processing...')
  })

  it('clears frozen streaming text when the backend reports no open stream', () => {
    useChatStore.setState({ streamingText: { [SESSION]: 'partial answer that end' } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: false, paused: false, streaming: false })

    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })

  it('preserves streaming text while the backend reports an open stream', () => {
    useChatStore.setState({ streamingText: { [SESSION]: 'partial answ' } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: false, paused: false, streaming: true })

    expect(useChatStore.getState().streamingText[SESSION]).toBe('partial answ')
  })

  it('clears activity and streaming for a session with no running task', () => {
    useChatStore.setState({
      activityStatus: { [SESSION]: 'Thinking...' },
      streamingText: { [SESSION]: 'stale partial' },
    })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    expect(useChatStore.getState().activityStatus[SESSION]).toBeUndefined()
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })

  it('shows the paused label and clears streaming for a cooperatively paused task', () => {
    useChatStore.setState({ streamingText: { [SESSION]: 'frozen mid-stream' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: true })

    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Paused')
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })

  it('restores the running flag for an active session and leaves prompts untouched', () => {
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: false })

    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
    expect(sessionMessages()[0]!.metadata?.resolved).toBeUndefined()
  })

  it('restores the paused flag for a paused task without injecting a resume banner', () => {
    // A cooperatively paused task is a clean checkpoint: set paused, clear
    // taskActive, and do NOT inject the task_failed_resumable banner (it is
    // resumable via the Resume button / nudge, not a "did not finish" banner).
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: true })

    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
    expect(sessionMessages().some(m => m.type === 'task_failed_resumable')).toBe(false)
  })

  it('clears a stale pause-in-flight flag when the task is paused (pause landed in the background)', () => {
    // The user clicked Pause and switched sessions before the step boundary;
    // the pause landed with no listener, so on switch-back the in-flight
    // spinner flag is stale while the backend reports paused.
    useChatStore.setState({ pausing: { [SESSION]: true }, taskActive: { [SESSION]: true } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: true })

    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().pausing[SESSION]).toBeUndefined()
    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
  })

  it('renders the active UI (never paused) when the snapshot carries a stale paused flag', () => {
    // Race: the snapshot was read before a resume that landed in the
    // background, and session_resumed arrived before the snapshot was
    // applied — the reconcile sees active=true with a leftover paused=true.
    // `active` wins: the paused branch is skipped, the ghost paused flag is
    // cleared, and the live task shows no white dot / "Paused" label / Resume
    // button.
    useChatStore.setState({ paused: { [SESSION]: true }, taskActive: { [SESSION]: false } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: true })

    expect(useChatStore.getState().paused[SESSION]).toBeUndefined()
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
    expect(useChatStore.getState().activityStatus[SESSION]).not.toBe('Paused')
  })

  it('preserves a pause-in-flight flag for a still-running task (pause not yet landed)', () => {
    useChatStore.setState({ pausing: { [SESSION]: true } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: false })

    expect(useChatStore.getState().pausing[SESSION]).toBe(true)
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
    expect(useChatStore.getState().paused[SESSION]).toBeUndefined()
  })

  it('resolves stale HITL prompts for a paused task without injecting a resume banner', () => {
    // A paused task with a lingering step_limit prompt: the executor waiting
    // for the response no longer exists, so the prompt must be resolved as
    // stale — consistent with the unfinished-task path. No banner is injected.
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    const stale = reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: true })

    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
    const stepLimit = sessionMessages().find(m => m.type === 'step_limit')
    expect(stepLimit?.metadata?.resolved).toBe(true)
    expect(stepLimit?.metadata?.stale).toBe(true)
    expect(sessionMessages().some(m => m.type === 'task_failed_resumable')).toBe(false)
    expect(stale).toHaveLength(1)
    expect(stale[0]!.type).toBe('step_limit')
  })

  it('injects a resume banner when an unfinished task exists and none is pending', () => {
    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
    const msgs = sessionMessages()
    expect(msgs).toHaveLength(1)
    expect(msgs[0]!.type).toBe('task_failed_resumable')
    expect(msgs[0]!.metadata?.resolved).toBe(false)
  })

  it('does not duplicate an existing unresolved resume banner', () => {
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    expect(sessionMessages().filter(m => m.type === 'task_failed_resumable')).toHaveLength(1)
  })

  it('resolves stale step_limit prompts when the task is resumable but not running', () => {
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    const stepLimit = sessionMessages().find(m => m.type === 'step_limit')
    expect(stepLimit?.metadata?.resolved).toBe(true)
    expect(stepLimit?.metadata?.stale).toBe(true)
  })

  it('resolves stale resumable and step_limit prompts when nothing is unfinished', () => {
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false } })
    addMsg({ id: 'sl-1', type: 'step_limit', metadata: { request_id: 'r1' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    for (const msg of sessionMessages()) {
      expect(msg.metadata?.resolved).toBe(true)
    }
    expect(sessionMessages()).toHaveLength(2) // no banner injected
  })

  // --- stale plan_review / tool_confirm / ask_user after reload ---
  // These interactive prompts store `resolved: true` only in the in-memory
  // Zustand store — the flag is never persisted to the backend. After an app
  // reload the history comes back without `resolved`, so reconcileRuntimeStatus
  // must dismiss them as stale when the session is no longer active.

  it('resolves stale plan_review when the session completed successfully', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.stale).toBe(true)
  })

  it('resolves stale plan_review when the task is resumable but not running', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.stale).toBe(true)
  })

  it('resolves stale tool_confirm and ask_user when nothing is unfinished', () => {
    addMsg({ id: 'tc-1', type: 'tool_confirm', metadata: { confirm_id: 'c1' } })
    addMsg({ id: 'au-1', type: 'ask_user', metadata: { request_id: 'r1', questions: [] } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    const toolConfirm = sessionMessages().find(m => m.type === 'tool_confirm')
    expect(toolConfirm?.metadata?.resolved).toBe(true)
    expect(toolConfirm?.metadata?.stale).toBe(true)

    const askUser = sessionMessages().find(m => m.type === 'ask_user')
    expect(askUser?.metadata?.resolved).toBe(true)
    expect(askUser?.metadata?.stale).toBe(true)
  })

  it('leaves plan_review untouched when the session is still active', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: false } })

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: false, paused: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(false)
  })

  it('does not re-resolve already-resolved plan_review', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1', resolved: true, decision: 'approve' } })

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    const planReview = sessionMessages().find(m => m.type === 'plan_review')
    expect(planReview?.metadata?.resolved).toBe(true)
    expect(planReview?.metadata?.decision).toBe('approve')
    expect(planReview?.metadata?.stale).toBeUndefined() // not touched
  })

  it('returns the resolved stale messages so the caller can persist them', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    addMsg({ id: 'resume-1', type: 'task_failed_resumable', metadata: { resolved: false, task_id: 't-9' } })

    const stale = reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    // Both resolved messages are returned, with their identifying metadata
    // intact, so the caller can map type → match field and persist.
    expect(stale).toHaveLength(2)
    expect(stale.map(m => m.type).sort()).toEqual(['plan_review', 'task_failed_resumable'])
    const resumable = stale.find(m => m.type === 'task_failed_resumable')!
    expect(resumable.metadata?.task_id).toBe('t-9')
  })

  it('returns an empty list (and does not mutate) for an active session', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })

    const stale = reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: false })

    expect(stale).toHaveLength(0)
  })

  // --- stale-snapshot guard (snapshotReadAt) ---

  it('keeps a newer live label and open stream over a stale snapshot', () => {
    // An assistant_chunk lands while the status RPC is in flight (the event
    // subscription mounts before the RPC resolves): the live handler updated
    // the label and streaming text, stamping runtimeEventAt AFTER the caller
    // read the snapshot. The snapshot's older phase must not roll the label
    // back nor wipe the accumulating stream.
    useChatStore.setState({ streamingText: { [SESSION]: 'newer live partial' } })
    useChatStore.getState().setActivityStatus(SESSION, 'Generating response...')

    reconcileRuntimeStatus(
      SESSION,
      { active: true, has_unfinished_task: false, paused: false, activity: 'Thinking...', streaming: false },
      Date.now() - 5,
    )

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Generating response...')
    expect(useChatStore.getState().streamingText[SESSION]).toBe('newer live partial')
    // taskActive still comes from the snapshot (assistant_chunk does not own it).
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
  })

  it('applies the snapshot label/streaming when no live event beat the read', () => {
    // Same shape as above, but the read is NEWER than the last live mark —
    // the snapshot is the freshest knowledge and applies normally.
    useChatStore.setState({ streamingText: { [SESSION]: 'frozen partial' } })
    useChatStore.getState().setActivityStatus(SESSION, 'Routing request...')

    reconcileRuntimeStatus(
      SESSION,
      { active: true, has_unfinished_task: false, paused: false, activity: 'Thinking...', streaming: false },
      Date.now() + 5_000,
    )

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Thinking...')
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })

  it('keeps a live label over the paused snapshot but still sets the paused flag', () => {
    // The pause landed live on switch-back (session_paused arrived after the
    // snapshot was read): its label survives; the paused flag itself still
    // comes from the snapshot, which stays authoritative for it.
    useChatStore.getState().setActivityStatus(SESSION, 'Paused')

    reconcileRuntimeStatus(
      SESSION,
      { active: false, has_unfinished_task: true, paused: true },
      Date.now() - 5,
    )

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Paused')
    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
  })

  it('keeps a live terminal clear over a stale active snapshot', () => {
    // task_complete arrived live while the RPC was in flight and already
    // cleared the label/stream; an older active=true snapshot must not
    // resurrect them.
    useChatStore.setState({ streamingText: { [SESSION]: 'flushed away' } })
    useChatStore.getState().clearStreamingText(SESSION)
    useChatStore.getState().setActivityStatus(SESSION, null)

    reconcileRuntimeStatus(
      SESSION,
      { active: true, has_unfinished_task: false, paused: false, activity: 'Thinking...', streaming: true },
      Date.now() - 5,
    )

    expect(useChatStore.getState().activityStatus[SESSION]).toBeUndefined()
    expect(useChatStore.getState().streamingText[SESSION]).toBeUndefined()
  })
})

describe('reconcileRuntimeStatus → has_unfinished_task mirror', () => {
  beforeEach(resetStore)

  function seedSession(flag: boolean): void {
    useSessionStore.setState({
      sessions: [{
        id: SESSION,
        project_id: 'proj-1',
        name: 'Session',
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
        archived: false,
        pinned: false,
        active: false,
        total_input_tokens: 0,
        total_output_tokens: 0,
        model: '',
        family: '',
        has_unfinished_task: flag,
        unfinished_task_status: '',
      }],
    })
  }

  it('clears a stale unfinished flag when the snapshot reports no unfinished task', () => {
    // Bug: the list flag was a snapshot from list-load time; a session whose
    // task finished while unviewed stayed "busy" for archive/delete until an
    // app restart. The switch reconcile must refresh it from the snapshot.
    seedSession(true)

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: false, paused: false })

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(false)
  })

  it('restores the unfinished flag when the snapshot reports a resumable task', () => {
    seedSession(false)

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(true)
  })

  it('mirrors the flag for an actively running session too (snapshot authoritative)', () => {
    seedSession(false)

    reconcileRuntimeStatus(SESSION, { active: true, has_unfinished_task: true, paused: false })

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(true)
  })

  it('keeps the sessions reference when the flag value is unchanged (stable selectors)', () => {
    seedSession(true)
    const before = useSessionStore.getState().sessions

    reconcileRuntimeStatus(SESSION, { active: false, has_unfinished_task: true, paused: false })

    expect(useSessionStore.getState().sessions).toBe(before)
  })
})

describe('reconcilePendingActions', () => {
  beforeEach(resetStore)

  it('surfaces a waiting-for-response label when prompts are genuinely pending', () => {
    reconcilePendingActions(SESSION, {
      tool_confirms: [],
      step_limits: [],
      plan_approvals: [],
      ask_user: [{ request_id: 'r1', questions: [{ question: 'Proceed?', options: [] }] }],
      goal_proposals: [],
    } as unknown as PendingActionsResponse)

    expect(useChatStore.getState().activityStatus[SESSION]).toBe('Waiting for your response...')
  })

  // After an app restart the in-memory pending maps are empty, so every
  // persisted HITL prompt is reported as "not pending" and must be resolved.
  const EMPTY_PENDING: PendingActionsResponse = {
    tool_confirms: [],
    step_limits: [],
    plan_approvals: [],
    ask_user: [],
    goal_proposals: [],
  }

  it('resolves HITL prompts absent from the pending set and returns them', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    addMsg({ id: 'tc-1', type: 'tool_confirm', metadata: { confirm_id: 'c1' } })

    const stale = reconcilePendingActions(SESSION, EMPTY_PENDING)

    expect(stale.map(m => m.type).sort()).toEqual(['plan_review', 'tool_confirm'])
    for (const msg of sessionMessages()) {
      expect(msg.metadata?.resolved).toBe(true)
      expect(msg.metadata?.stale).toBe(true)
    }
  })

  it('leaves prompts untouched when they are still reported as pending', () => {
    addMsg({ id: 'pr-1', type: 'plan_review', metadata: { request_id: 'r1' } })
    const pending: PendingActionsResponse = {
      ...EMPTY_PENDING,
      plan_approvals: [{ request_id: 'r1', plan_path: '', plan_content: 'plan' }],
    }

    const stale = reconcilePendingActions(SESSION, pending)

    expect(stale).toHaveLength(0)
    expect(sessionMessages()[0]!.metadata?.resolved).toBeUndefined()
  })

  it('adds missing pending messages not present in the store', () => {
    const pending: PendingActionsResponse = {
      ...EMPTY_PENDING,
      ask_user: [{ request_id: 'r2', questions: [{ id: 'q1', question: 'Pick one', options: [{ label: 'A', value: 'a' }] }] }],
    }

    reconcilePendingActions(SESSION, pending)

    const added = sessionMessages().find(m => m.type === 'ask_user')
    expect(added?.id).toBe('ask-user-r2')
    expect(added?.metadata?.request_id).toBe('r2')
  })
})

describe('stalePromptMatchField', () => {
  it('maps each HITL type to its identifying metadata field', () => {
    expect(stalePromptMatchField('tool_confirm')).toBe('confirm_id')
    expect(stalePromptMatchField('ask_user')).toBe('request_id')
    expect(stalePromptMatchField('step_limit')).toBe('request_id')
    expect(stalePromptMatchField('plan_review')).toBe('request_id')
    expect(stalePromptMatchField('task_failed_resumable')).toBe('task_id')
  })

  it('returns null for non-persistable types', () => {
    expect(stalePromptMatchField('assistant')).toBeNull()
    expect(stalePromptMatchField('error')).toBeNull()
  })
})

describe('refreshCompactionNoOp', () => {
  beforeEach(() => {
    vi.mocked(getSessionRuntimeStatus).mockReset()
    resetStore()
  })

  it('applies the fetched verdict (true and false)', async () => {
    vi.mocked(getSessionRuntimeStatus).mockResolvedValue({
      active: false,
      has_unfinished_task: false,
      paused: false,
      compaction_noop: true,
    })
    refreshCompactionNoOp(SESSION)
    await vi.waitFor(() => expect(useChatStore.getState().compactionNoOp[SESSION]).toBe(true))

    // A finished task grew the history past the target: the refetch lifts the
    // disabled state without a session switch.
    vi.mocked(getSessionRuntimeStatus).mockResolvedValue({
      active: false,
      has_unfinished_task: false,
      paused: false,
      compaction_noop: false,
    })
    refreshCompactionNoOp(SESSION)
    await vi.waitFor(() => expect(useChatStore.getState().compactionNoOp[SESSION]).toBeUndefined())
  })

  it('keeps the previous flag when the RPC fails or returns null (best-effort)', async () => {
    useChatStore.setState({ compactionNoOp: { [SESSION]: true } })

    vi.mocked(getSessionRuntimeStatus).mockResolvedValue(null)
    refreshCompactionNoOp(SESSION)
    await vi.waitFor(() => expect(getSessionRuntimeStatus).toHaveBeenCalled())
    expect(useChatStore.getState().compactionNoOp[SESSION]).toBe(true)

    vi.mocked(getSessionRuntimeStatus).mockRejectedValue(new Error('rpc down'))
    refreshCompactionNoOp(SESSION)
    await vi.waitFor(() => expect(getSessionRuntimeStatus).toHaveBeenCalled())
    expect(useChatStore.getState().compactionNoOp[SESSION]).toBe(true)
  })
})

describe('reconcileRuntimeStatus → stale-snapshot guard for mirror flags', () => {
  beforeEach(resetStore)

  function seedSession(flag: boolean): void {
    useSessionStore.setState({
      sessions: [{
        id: SESSION,
        project_id: 'proj-1',
        name: 'Session',
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
        archived: false,
        pinned: false,
        active: false,
        total_input_tokens: 0,
        total_output_tokens: 0,
        model: '',
        family: '',
        has_unfinished_task: flag,
        unfinished_task_status: '',
      }],
    })
  }

  it('does not re-lock the input when a live compaction_finished beats the snapshot', () => {
    // Switch back to a session whose manual compaction just finished: the
    // live compaction_finished handler unlocked the input (setCompacting
    // false) AFTER the status RPC was read but BEFORE it resolved. The
    // snapshot still says compacting=true — it must not re-lock.
    const readAt = Date.now()
    useChatStore.getState().setCompacting(SESSION, true)
    useChatStore.getState().setCompacting(SESSION, false) // live finished (stamps)

    reconcileRuntimeStatus(
      SESSION,
      { active: false, has_unfinished_task: false, paused: false, compacting: true },
      readAt - 10,
    )

    expect(useChatStore.getState().compacting[SESSION]).toBeUndefined()
  })

  it('does not resurrect the unfinished flag when a live terminal event beats the snapshot', () => {
    // A live task_complete cleared the flag; its setActivityStatus(null) on
    // an already-null label must still stamp runtimeEventAt, and the stale
    // snapshot (has_unfinished_task=true, read before completion) must not
    // re-arm the archive/delete busy state.
    seedSession(false)
    const readAt = Date.now()
    useChatStore.getState().setActivityStatus(SESSION, null) // no-op clear — must stamp

    reconcileRuntimeStatus(
      SESSION,
      { active: false, has_unfinished_task: true, paused: false },
      readAt - 10,
    )

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(false)
  })

  it('does not revert a live pause transition that beat the snapshot', () => {
    // The REAL live session_paused handler sets flags AND label; a snapshot
    // read before the pause landed must not flip them back to running.
    const readAt = Date.now() - 10
    handleSessionPausedEvent(SESSION)

    reconcileRuntimeStatus(
      SESSION,
      { active: true, has_unfinished_task: true, paused: false },
      readAt,
    )

    expect(useChatStore.getState().paused[SESSION]).toBe(true)
    expect(useChatStore.getState().taskActive[SESSION]).toBe(false)
  })

  it('does not revert a live resume transition that beat the snapshot', () => {
    // session_resumed landed after the snapshot was read: the snapshot still
    // says paused and must not re-paint the paused UI over the live task.
    const readAt = Date.now() - 10
    handleSessionPausedEvent(SESSION)
    handleSessionResumedEvent(SESSION)

    reconcileRuntimeStatus(
      SESSION,
      { active: false, has_unfinished_task: true, paused: true },
      readAt,
    )

    expect(useChatStore.getState().paused[SESSION]).toBeUndefined()
    expect(useChatStore.getState().taskActive[SESSION]).toBe(true)
  })

  it('still mirrors the flags when no live event intervened', () => {
    seedSession(true)

    reconcileRuntimeStatus(
      SESSION,
      { active: false, has_unfinished_task: false, paused: false, compacting: true },
      Date.now() + 1000, // snapshot newer than any live mark
    )

    expect(useSessionStore.getState().sessions![0]!.has_unfinished_task).toBe(false)
    expect(useChatStore.getState().compacting[SESSION]).toBe(true)
  })
})
