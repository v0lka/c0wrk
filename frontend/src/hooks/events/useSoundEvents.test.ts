// @vitest-environment jsdom
//
// Unit tests for the sound-event hook.
//
// `classifySessionEvent` is a pure mapping and is tested without any runtime.
// `useSoundEvents` is additionally pinned to NOT own the audio unlock: the
// persistent gesture/visibility listeners are registered once at App start
// (App.tsx), so they exist even with no active session — a state in which this
// hook is a no-op. Here we verify the hook only wires session subscriptions.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The unlock initialiser lives in lib/sound, but ownership must belong to App —
// spy on it here to prove the hook never calls it. playSound is stubbed so no
// real audio work happens.
vi.mock('@/lib/sound', () => ({
  playSound: vi.fn(),
  initSoundUnlock: vi.fn(),
}))
vi.mock('@/api/runtime', () => ({
  onSessionEvent: vi.fn(() => () => {}),
}))

// The notification engine is mocked so notifySessionCue tests observe the
// dispatch (content + context) without any Go-bridge transport. The label
// resolution reads the REAL stores via getState() — jsdom provides document,
// and the stores' getState surface is enough for these pure-read tests (no
// RPC subscriptions fire on getState).
vi.mock('@/lib/systemNotifications', () => ({
  classifyNotificationContent: (event: SessionEventKey, data: unknown) => {
    // A minimal faithful copy of the production mapping for the events these
    // tests use — enough to assert the title composition and suppression.
    if (event === 'task_complete') {
      const success = (data as { success?: boolean } | null | undefined)?.success
      return success === false
        ? { kind: 'error', title: 'Task failed', body: 'The task finished with errors.' }
        : { kind: 'success', title: 'Task completed', body: 'The task finished successfully.' }
    }
    if (event === 'tool_confirm') {
      return { kind: 'attention', title: 'Tool approval required', body: 'A tool call needs your confirmation.' }
    }
    return null
  },
  sendSystemNotification: vi.fn(),
}))

import { classifySessionEvent, isSessionNotificationRedundant, notifySessionCue, useSoundEvents } from '@/hooks/events/useSoundEvents'
import { initSoundUnlock } from '@/lib/sound'
import { sendSystemNotification } from '@/lib/systemNotifications'
import { onSessionEvent } from '@/api/runtime'
import { useSessionStore } from '@/stores/sessionStore'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import type { SessionInfo } from '@/types/models'
import type { SessionEventKey } from '@/types/events'

/** A minimal session snapshot entry for label resolution. */
const sessionS1: SessionInfo = {
  id: 's1',
  project_id: 'p1',
  name: 'my-session',
  created_at: '2026-01-01T00:00:00Z',
  last_active_at: '2026-01-01T00:00:00Z',
  archived: false,
  pinned: false,
  active: false,
  total_input_tokens: 0,
  total_output_tokens: 0,
  model: 'test-model',
  family: 'test',
  has_unfinished_task: false,
  unfinished_task_status: '',
}

describe('classifySessionEvent', () => {
  it('maps task_complete (success) to a success tone', () => {
    expect(classifySessionEvent('task_complete', { success: true })).toBe('success')
  })

  it('maps task_complete (default success) to a success tone', () => {
    // success is optional; its absence means a clean finish.
    expect(classifySessionEvent('task_complete', { output: 'done' })).toBe('success')
  })

  it('maps a degraded task_complete (success:false) to an error tone, not success', () => {
    // A partial/failed/aborted execution must NOT sound positive.
    expect(
      classifySessionEvent('task_complete', { success: false, completion: 'partial' }),
    ).toBe('error')
  })

  it('maps interactive prompts to the attention tone', () => {
    const attentionEvents: SessionEventKey[] = [
      'ask_user',
      'step_limit',
      'tool_confirm',
      'plan_review_ready',
      'task_failed_resumable',
      'goal_proposal',
    ]
    for (const event of attentionEvents) {
      expect(classifySessionEvent(event, {})).toBe('attention')
    }
  })

  it('maps error and task_cancelled to the error tone', () => {
    expect(classifySessionEvent('error', { error: 'boom' })).toBe('error')
    expect(classifySessionEvent('task_cancelled', undefined)).toBe('error')
  })

  it('returns null (silent) for progress / streaming / lifecycle events', () => {
    // These churn constantly during a run — never audible.
    const silentEvents: SessionEventKey[] = [
      'routing',
      'step_start',
      'step_complete',
      'thought',
      'tool_call',
      'tool_result',
      'assistant_chunk',
      'assistant_done',
      'plan_step_start',
      'plan_step_complete',
      'context_fill',
      'reflection',
      'goal_status',
      'goal_progress',
    ]
    for (const event of silentEvents) {
      expect(classifySessionEvent(event, {})).toBeNull()
    }
  })

  it('handles null/undefined data defensively', () => {
    expect(classifySessionEvent('task_complete', null)).toBe('success')
    expect(classifySessionEvent('task_complete', undefined)).toBe('success')
    expect(classifySessionEvent('ask_user', null)).toBe('attention')
    expect(classifySessionEvent('error', null)).toBe('error')
  })
})

/** Mount the hook via a minimal probe component and return an unmount fn. */
async function mountSoundEvents(sessionId: string | null): Promise<() => Promise<void>> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root: Root = createRoot(container)

  function Probe(): null {
    useSoundEvents(sessionId)
    return null
  }

  await act(async () => {
    // createElement (not JSX) so this stays a .ts file.
    root.render(createElement(Probe))
  })

  return async () => {
    await act(async () => {
      root.unmount()
    })
    container.remove()
  }
}

describe('useSoundEvents — unlock ownership', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('does not initialise the unlock and subscribes to nothing without a session', async () => {
    const unmount = await mountSoundEvents(null)
    expect(vi.mocked(initSoundUnlock)).not.toHaveBeenCalled()
    expect(vi.mocked(onSessionEvent)).not.toHaveBeenCalled()
    await unmount()
  })

  it('subscribes to the session events but still never owns the unlock', async () => {
    const unmount = await mountSoundEvents('s1')
    expect(vi.mocked(onSessionEvent)).toHaveBeenCalled()
    // Ownership moved to App: the hook must not register the unlock itself.
    expect(vi.mocked(initSoundUnlock)).not.toHaveBeenCalled()
    await unmount()
  })
})

// --- Banner dispatch: suppression + title composition ----------------------
//
// notifySessionCue is the single dispatch point for BOTH hooks (the active
// session's useSoundEvents and the background watcher's announceBackgroundCue),
// so its suppression rule and title format are pinned here once for both.

describe('notifySessionCue — suppression (Telegram rule)', () => {
  const sendMock = vi.mocked(sendSystemNotification)
  let focusSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    vi.clearAllMocks()
    // jsdom's document.hasFocus() is false by default (no real window focus),
    // so each test pins the focus state it needs explicitly.
    focusSpy = vi.spyOn(document, 'hasFocus')
    focusSpy.mockReturnValue(true)
    // Seed both label sources so 's1' resolves to 'my-session'.
    useActiveSessionsStore.setState({ sessions: [sessionS1] })
    useSessionStore.setState({ sessions: [sessionS1], activeSessionId: null })
  })

  afterEach(() => {
    focusSpy.mockRestore()
  })

  it('suppresses the banner when the window is focused and that session is active', () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    notifySessionCue('task_complete', { success: true }, { sessionId: 's1' })
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('sends the banner when the window is focused but a DIFFERENT session is active', () => {
    useSessionStore.setState({ activeSessionId: 'other' })
    notifySessionCue('task_complete', { success: true }, { sessionId: 's1' })
    expect(sendMock).toHaveBeenCalledTimes(1)
  })

  it('sends the banner when the window is unfocused even if the session is active', () => {
    focusSpy.mockReturnValue(false)
    useSessionStore.setState({ activeSessionId: 's1' })
    notifySessionCue('task_complete', { success: true }, { sessionId: 's1' })
    expect(sendMock).toHaveBeenCalledTimes(1)
  })

  it('sends the banner when no session is active even while focused', () => {
    useSessionStore.setState({ activeSessionId: null })
    notifySessionCue('task_complete', { success: true }, { sessionId: 's1' })
    expect(sendMock).toHaveBeenCalledTimes(1)
  })
})

describe('isSessionNotificationRedundant', () => {
  let focusSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    focusSpy = vi.spyOn(document, 'hasFocus')
    focusSpy.mockReturnValue(true)
  })

  afterEach(() => {
    focusSpy.mockRestore()
  })

  it('returns false when the document is unfocused', () => {
    focusSpy.mockReturnValue(false)
    useSessionStore.setState({ activeSessionId: 's1' })
    expect(isSessionNotificationRedundant('s1')).toBe(false)
  })

  it('returns true only for focused + that exact session active', () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    expect(isSessionNotificationRedundant('s1')).toBe(true)
    expect(isSessionNotificationRedundant('s2')).toBe(false)
  })
})

describe('notifySessionCue — title composition (event first)', () => {
  const sendMock = vi.mocked(sendSystemNotification)

  beforeEach(() => {
    vi.clearAllMocks()
    useActiveSessionsStore.setState({ sessions: [sessionS1] })
    useSessionStore.setState({ sessions: [sessionS1], activeSessionId: null })
  })

  it('leads with the event title and appends the session name', () => {
    useSessionStore.setState({ activeSessionId: 'other' })
    notifySessionCue('task_complete', { success: true }, { sessionId: 's1' })
    expect(sendMock).toHaveBeenCalledTimes(1)
    const [content] = sendMock.mock.calls[0]!
    expect(content.title).toBe('Task completed — my-session')
    expect(content.title).not.toMatch(/^my-session/)
  })

  it('falls back to the app name when the session cannot be resolved', () => {
    useSessionStore.setState({ activeSessionId: 'other' })
    notifySessionCue('tool_confirm', {}, { sessionId: 'unknown-session' })
    expect(sendMock).toHaveBeenCalledTimes(1)
    const [content] = sendMock.mock.calls[0]!
    expect(content.title).toBe('Tool approval required — c0wrk')
  })
})
