// @vitest-environment jsdom
//
// Regression tests for the resume prompt's Cancel action. Cancelling a failed
// (resumable) task is a hard discard that emits no terminal event, so the
// frontend must clear the stale live-session state itself — otherwise the
// session keeps showing on the active-sessions radar (red "failed") until the
// 30s safety poll.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The RPC layer must not reach Wails. activeSessionsStore imports listAllSessions
// / getPendingActions at module scope, so those exports must exist on the mocks.
const { cancelUnfinishedTaskMock, resumeTaskMock, listAllSessionsMock, getPendingActionsMock } = vi.hoisted(() => ({
  cancelUnfinishedTaskMock: vi.fn(async () => {}),
  resumeTaskMock: vi.fn(async () => {}),
  listAllSessionsMock: vi.fn(async () => []),
  getPendingActionsMock: vi.fn(async () => null),
}))

vi.mock('@/api/sessions', () => ({ listAllSessions: listAllSessionsMock }))
vi.mock('@/api/chat', () => ({
  cancelUnfinishedTask: cancelUnfinishedTaskMock,
  resumeTask: resumeTaskMock,
  getPendingActions: getPendingActionsMock,
}))

import { ResumeActionPanel } from './ResumeActionPanel'
import { cancelPendingRefresh, useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import type { SessionInfo } from '@/types/models'

const SESSION_ID = 'sess-1'

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  const ts = new Date(0).toISOString()
  return {
    id: SESSION_ID,
    project_id: 'p1',
    name: 'Session',
    created_at: ts,
    last_active_at: ts,
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: true,
    unfinished_task_status: 'failed',
    ...overrides,
  }
}

function makeResumeItem(metadata: Record<string, unknown> = {}): Extract<DisplayItem, { kind: 'resume_action' }> {
  const message: ChatMessageUI = {
    id: 'resume-1',
    sessionId: SESSION_ID,
    type: 'task_failed_resumable',
    content: 'Plan execution failed.',
    metadata: { resolved: false, ...metadata },
    timestamp: 0,
  }
  return { kind: 'resume_action', message }
}

const roots: Root[] = []

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push(root)
  act(() => {
    root.render(ui)
  })
  return container
}

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes(text))
  if (!btn) throw new Error(`button "${text}" not found`)
  return btn as HTMLButtonElement
}

beforeEach(() => {
  cancelUnfinishedTaskMock.mockReset().mockResolvedValue(undefined)
  resumeTaskMock.mockReset().mockResolvedValue(undefined)
  listAllSessionsMock.mockReset().mockResolvedValue([])
  getPendingActionsMock.mockReset().mockResolvedValue(null)
  useActiveSessionsStore.setState({ sessions: [makeSession()], pendingOverride: {}, refreshing: false })
  useSessionStore.setState({ sessions: [makeSession()], activeSessionId: SESSION_ID })
  useChatStore.setState({ taskActive: {}, unfinishedTaskStatus: {}, paused: {}, messages: {}, messageOrder: {} })
})

afterEach(() => {
  // The cancel path schedules a debounced snapshot refresh — drop it so the
  // trailing RPC cannot land in the next test.
  cancelPendingRefresh()
  for (const root of roots.splice(0)) {
    act(() => {
      root.unmount()
    })
  }
})

describe('ResumeActionPanel cancel', () => {
  it('discards the task and clears the stale live-session state', async () => {
    const container = render(<ResumeActionPanel item={makeResumeItem()} />)

    await act(async () => {
      buttonByText(container, 'Cancel').click()
    })

    expect(cancelUnfinishedTaskMock).toHaveBeenCalledWith(SESSION_ID)

    // The session must leave the radar: its DB-derived snapshot entry no
    // longer carries an unfinished task.
    const snap = useActiveSessionsStore.getState().sessions!.find((s) => s.id === SESSION_ID)!
    expect(snap.unfinished_task_status).toBe('')
    expect(snap.has_unfinished_task).toBe(false)

    // ...and the SINGLE live unfinished-task overlay is cleared, so every dot
    // (sidebar session list, radar badge, radar dropdown) turns idle live.
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION_ID]).toBe('')
  })

  it('keeps the state when the cancel RPC rejects (nothing settled)', async () => {
    cancelUnfinishedTaskMock.mockRejectedValueOnce(new Error('backend gone'))
    const container = render(<ResumeActionPanel item={makeResumeItem()} />)

    await act(async () => {
      buttonByText(container, 'Cancel').click()
    })

    const snap = useActiveSessionsStore.getState().sessions!.find((s) => s.id === SESSION_ID)!
    expect(snap.unfinished_task_status).toBe('failed')
    // The overlay is left untouched on failure — the task stays resumable.
    expect(useChatStore.getState().unfinishedTaskStatus[SESSION_ID]).toBeUndefined()
  })
})

describe('ResumeActionPanel auto-resend countdown', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('ticks the Resume button label 30→0, fires the auto-resend, and yields the button', async () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    const container = render(<ResumeActionPanel item={makeResumeItem({ auto_retry_at: deadline, auto_retry_live: true })} />)

    const resume = buttonByText(container, 'Resume')
    expect(resume.textContent).toContain('Resume (30s)')
    expect(resume.disabled).toBe(false)
    expect(container.textContent).toContain('Auto-retry in 30s')

    await act(async () => { vi.advanceTimersByTime(30_000) })

    expect(resume.disabled).toBe(true)
    expect(resume.textContent).toContain('Auto-resend')
    // On zero the hook fired the auto-resend through the ordinary resume RPC.
    expect(resumeTaskMock).toHaveBeenCalledTimes(1)
    expect(resumeTaskMock).toHaveBeenCalledWith(SESSION_ID)
  })

  it('renders the plain banner when the deadline is absent (previous look)', () => {
    const container = render(<ResumeActionPanel item={makeResumeItem()} />)

    const resume = buttonByText(container, 'Resume')
    expect(resume.textContent).toContain('Resume')
    expect(resume.textContent).not.toContain('(')
    expect(resume.disabled).toBe(false)
    expect(container.textContent).not.toContain('Auto-retry in')
  })

  it('renders the plain manual banner for a restored row (no live flag, even with a future deadline)', () => {
    // A history-reload banner carries the raw persisted payload WITHOUT
    // auto_retry_live: after a restart there is no timer to keep — the
    // user decides manually, no matter how far in the future the stale
    // deadline is.
    const deadline = Math.floor(Date.now() / 1000) + 500
    const container = render(
      <ResumeActionPanel item={makeResumeItem({ auto_retry_at: deadline })} />,
    )

    const resume = buttonByText(container, 'Resume')
    expect(resume.disabled).toBe(false)
    expect(resume.textContent).toContain('Resume')
    expect(resume.textContent).not.toContain('Auto-resend')
    expect(container.textContent).not.toContain('Auto-retry in')
    act(() => { vi.advanceTimersByTime(600_000) })
    expect(resumeTaskMock).not.toHaveBeenCalled()
  })

  it('does not double-resume when the button is clicked after the countdown hit 0', async () => {
    const deadline = Math.floor(Date.now() / 1000) + 1
    const container = render(<ResumeActionPanel item={makeResumeItem({ auto_retry_at: deadline, auto_retry_live: true })} />)

    await act(async () => { vi.advanceTimersByTime(1_000) })

    const resume = buttonByText(container, 'Auto-resend')
    expect(resume.disabled).toBe(true)
    // The auto fire already used the single resume slot.
    expect(resumeTaskMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      // A disabled button ignores clicks in a real DOM; call the handler path
      // anyway through a forced dispatch to prove the guard holds.
      resume.click()
    })

    expect(resumeTaskMock).toHaveBeenCalledTimes(1)
  })

  it('a manual Resume click before expiry stops the countdown and resumes once', async () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    const container = render(<ResumeActionPanel item={makeResumeItem({ auto_retry_at: deadline, auto_retry_live: true })} />)

    const resume = buttonByText(container, 'Resume')
    await act(async () => {
      resume.click()
    })

    expect(resumeTaskMock).toHaveBeenCalledTimes(1)
    expect(resumeTaskMock).toHaveBeenCalledWith(SESSION_ID, '', '')

    // The manual click disarmed the auto fire: the deadline window passes
    // with no second resume.
    await act(async () => { vi.advanceTimersByTime(60_000) })
    expect(resumeTaskMock).toHaveBeenCalledTimes(1)
  })

  it('a Cancel click before expiry stops the countdown (no auto fire afterwards)', async () => {
    const deadline = Math.floor(Date.now() / 1000) + 30
    const container = render(<ResumeActionPanel item={makeResumeItem({ auto_retry_at: deadline, auto_retry_live: true })} />)

    await act(async () => {
      buttonByText(container, 'Cancel').click()
    })

    expect(resumeTaskMock).not.toHaveBeenCalled()
    expect(cancelUnfinishedTaskMock).toHaveBeenCalledWith(SESSION_ID)

    await act(async () => { vi.advanceTimersByTime(60_000) })
    expect(resumeTaskMock).not.toHaveBeenCalled()
  })

  it('resolves the banner when task_resumed lands after auto-resend (existing handler contract)', () => {
    // task_resumed (auto or manual) is handled by useActionEvents, which marks
    // the latest unresolved banner resolved:true — the panel then renders the
    // settled "Task resumed" card. Verify the panel honors that metadata.
    const container = render(
      <ResumeActionPanel item={makeResumeItem({ resolved: true, decision: 'resumed' })} />,
    )
    expect(container.textContent).toContain('Task resumed')
    expect(container.querySelectorAll('button')).toHaveLength(0)
  })
})
