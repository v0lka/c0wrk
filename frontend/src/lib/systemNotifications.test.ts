// Unit tests for lib/systemNotifications.ts — the OS notification engine.
//
// The vitest environment is `node` (no DOM), so these tests install a minimal
// fake `window` (with the Wails runtime bindings) and drive the module
// directly, mirroring the discipline of sound.test.ts. The pure classification
// is tested without any runtime at all.

import { describe, it, expect, beforeEach, afterEach, vi, type MockInstance } from 'vitest'

// The engine's transport is the Go bridge (@/api/notifications); these tests
// mock that module directly. The hoisted localStorage window remains needed
// for the zustand persist middleware (captured at store-creation time —
// `createJSONStorage(() => window.localStorage)` — when
// `@/stores/systemNotificationStore` is imported below). This file runs in
// the node environment, where `window` does not exist yet, so without a
// storage present at import time every `setEnabled` warns "Unable to update
// item 'c0wrk-system-notifications'". Same pattern as sound.test.ts /
// themeStore.test.ts.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const map = new Map<string, string>()
  g.window = {
    localStorage: {
      getItem: (k: string): string | null => map.get(k) ?? null,
      setItem: (k: string, v: string): void => {
        map.set(k, v)
      },
      removeItem: (k: string): void => {
        map.delete(k)
      },
      clear: (): void => {
        map.clear()
      },
      key: (i: number): string | null => Array.from(map.keys())[i] ?? null,
      get length(): number {
        return map.size
      },
    },
  }
})

// Mock the Go notification transport BEFORE importing the module under test.
// init/send behavior is fully controlled here; the macOS authorization prompt
// now lives behind InitNotifications on the Go side, so this mock has no
// authorization surface at all.
const { initGoMock, sendGoMock } = vi.hoisted(() => ({
  initGoMock: vi.fn((): Promise<void> => Promise.resolve()),
  sendGoMock: vi.fn(
    (_title: string, _body: string, _data?: Record<string, string>): Promise<void> =>
      Promise.resolve(),
  ),
}))

vi.mock('@/api/notifications', () => ({
  initSystemNotifications: initGoMock,
  sendSystemNotification: sendGoMock,
  onNotificationClicked: vi.fn(),
  showTestNotification: vi.fn(),
}))

// isWailsReady gates the transport (absent bindings → silent no-op, exactly
// like the old window.runtime detection). Default true; tests opt out.
const { isWailsReadyMock } = vi.hoisted(() => ({ isWailsReadyMock: vi.fn((): boolean => true) }))
vi.mock('@/api/runtime', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/api/runtime')>()
  return { ...original, isWailsReady: isWailsReadyMock }
})

import {
  classifyNotificationContent,
  sendSystemNotification,
  initSystemNotifications,
  __resetSystemNotificationModule,
} from '@/lib/systemNotifications'
import { useSystemNotificationStore } from '@/stores/systemNotificationStore'
import { logger } from '@/lib/logger'
import type { SessionEventKey } from '@/types/events'

// Failure paths under test deliberately reject Go-bridge calls, and the module
// surfaces those anomalies via logger.warn / logger.info — expected output
// here, not a defect. Silence them so the run log stays signal-only; the
// assertions observe the transport mocks and store, not the logs.
let loggerWarnSpy: MockInstance
let loggerInfoSpy: MockInstance

beforeEach(() => {
  loggerWarnSpy = vi.spyOn(logger, 'warn').mockImplementation(() => {})
  loggerInfoSpy = vi.spyOn(logger, 'info').mockImplementation(() => {})
  __resetSystemNotificationModule()
  useSystemNotificationStore.getState().setEnabled(true)
  initGoMock.mockClear()
  initGoMock.mockResolvedValue(undefined)
  sendGoMock.mockClear()
  sendGoMock.mockResolvedValue(undefined)
  isWailsReadyMock.mockClear()
  isWailsReadyMock.mockReturnValue(true)
})

afterEach(() => {
  loggerWarnSpy.mockRestore()
  loggerInfoSpy.mockRestore()
  __resetSystemNotificationModule()
})

describe('classifyNotificationContent', () => {
  it('maps task_complete (success) to success content with the output as body', () => {
    const content = classifyNotificationContent('task_complete', {
      success: true,
      output: 'All green.',
    })
    expect(content).not.toBeNull()
    expect(content?.kind).toBe('success')
    expect(content?.title).toBe('Task completed')
    expect(content?.body).toContain('All green.')
  })

  it('maps task_complete (default success) to success content', () => {
    const content = classifyNotificationContent('task_complete', { output: 'done' })
    expect(content?.kind).toBe('success')
    expect(content?.title).toBe('Task completed')
  })

  it('maps a degraded task_complete (success:false) to error-flavored content', () => {
    const content = classifyNotificationContent('task_complete', {
      success: false,
      completion: 'partial',
      output: 'halfway there',
    })
    expect(content?.kind).toBe('error')
    expect(content?.title).toBe('Task failed')
    expect(content?.body).toContain('halfway there')
  })

  it('falls back to a generic body when task_complete carries no output', () => {
    const ok = classifyNotificationContent('task_complete', { success: true })
    expect(ok?.body.length).toBeGreaterThan(0)
    const bad = classifyNotificationContent('task_complete', { success: false })
    expect(bad?.body.length).toBeGreaterThan(0)
  })

  it('truncates the task_complete output to ~200 chars', () => {
    const long = 'x'.repeat(500)
    const content = classifyNotificationContent('task_complete', { output: long })
    expect(content?.body.length).toBeLessThanOrEqual(200)
    expect(content?.body.endsWith('…')).toBe(true)
  })

  it('maps task_failed_resumable to attention with the payload message as body', () => {
    const content = classifyNotificationContent('task_failed_resumable', {
      message: 'executor crashed',
    })
    expect(content?.kind).toBe('attention')
    expect(content?.title).toBe('Task failed — resumable')
    expect(content?.body).toContain('executor crashed')
  })

  it('maps error to error content with the payload error as body', () => {
    const content = classifyNotificationContent('error', { error: 'boom' })
    expect(content?.kind).toBe('error')
    expect(content?.title).toBe('Task error')
    expect(content?.body).toContain('boom')
  })

  it('maps task_cancelled to error content', () => {
    const content = classifyNotificationContent('task_cancelled', undefined)
    expect(content?.kind).toBe('error')
    expect(content?.title).toBe('Task cancelled')
  })

  it('returns a distinct title for every attention event', () => {
    const attentionEvents: SessionEventKey[] = [
      'ask_user',
      'step_limit',
      'tool_confirm',
      'plan_review_ready',
      'task_failed_resumable',
      'goal_proposal',
    ]
    const titles = new Set<string>()
    for (const event of attentionEvents) {
      const content = classifyNotificationContent(event, {})
      expect(content, `event ${event} should classify`).not.toBeNull()
      expect(content?.kind).toBe('attention')
      expect(content?.title.length).toBeGreaterThan(0)
      expect(content?.body.length).toBeGreaterThan(0)
      titles.add(content!.title)
    }
    // Six events → six distinct, non-empty titles (differentiated text per
    // type is the whole point of a banner, unlike a three-tone sound).
    expect(titles.size).toBe(attentionEvents.length)
  })

  it('returns null (silent) for progress / streaming / lifecycle events', () => {
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
      expect(classifyNotificationContent(event, {}), `event ${event} should be null`).toBeNull()
    }
  })

  it('handles null/undefined data defensively', () => {
    expect(classifyNotificationContent('task_complete', null)?.kind).toBe('success')
    expect(classifyNotificationContent('task_complete', undefined)?.kind).toBe('success')
    expect(classifyNotificationContent('ask_user', null)?.kind).toBe('attention')
    expect(classifyNotificationContent('error', null)?.kind).toBe('error')
    expect(classifyNotificationContent('task_failed_resumable', null)?.kind).toBe('attention')
  })
})

describe('sendSystemNotification', () => {
  it('is a silent no-op when the master toggle is off', async () => {
    useSystemNotificationStore.getState().setEnabled(false)
    await sendSystemNotification({ kind: 'success', title: 'T', body: 'B' }, { sessionId: 's1' })
    expect(sendGoMock).not.toHaveBeenCalled()
  })

  it('is a silent no-op without the Go bindings (vitest / SSR / dev-frontend)', async () => {
    isWailsReadyMock.mockReturnValue(false)
    await expect(
      sendSystemNotification({ kind: 'attention', title: 'T', body: 'B' }, { sessionId: 's1' }),
    ).resolves.toBeUndefined()
    expect(sendGoMock).not.toHaveBeenCalled()
  })

  it('sends the content and the session routing context through the Go bridge', async () => {
    const content = classifyNotificationContent('task_complete', { output: 'ok' })!
    await sendSystemNotification(content, { sessionId: 's1', projectId: 'p1' })
    expect(sendGoMock).toHaveBeenCalledTimes(1)
    expect(sendGoMock).toHaveBeenCalledWith('Task completed', expect.stringContaining('ok'), {
      sessionId: 's1',
      projectId: 'p1',
    })
  })

  it('sends without a data map when no context was supplied', async () => {
    await sendSystemNotification({ kind: 'error', title: 'T', body: 'B' })
    expect(sendGoMock).toHaveBeenCalledWith('T', 'B', undefined)
  })

  it('omits the projectId key when the context has none (no undefined values)', async () => {
    await sendSystemNotification({ kind: 'attention', title: 'T', body: 'B' }, { sessionId: 's1' })
    expect(sendGoMock).toHaveBeenCalledWith('T', 'B', { sessionId: 's1' })
  })

  it('never throws when the Go send rejects', async () => {
    sendGoMock.mockRejectedValueOnce(new Error('D-Bus connect failed'))
    await expect(
      sendSystemNotification({ kind: 'error', title: 'T', body: 'B' }, { sessionId: 's1' }),
    ).resolves.toBeUndefined()
    expect(loggerWarnSpy).toHaveBeenCalled()
  })
})

describe('initSystemNotifications', () => {
  it('is idempotent: a second call is a no-op (runs once per app run)', async () => {
    await initSystemNotifications()
    await initSystemNotifications()
    expect(initGoMock).toHaveBeenCalledTimes(1)
  })

  it('shares one in-flight promise across concurrent calls', async () => {
    // Both invocations race before the first settles — only one init may run.
    await Promise.all([initSystemNotifications(), initSystemNotifications()])
    expect(initGoMock).toHaveBeenCalledTimes(1)
  })

  it('never throws and skips the bridge when the Go bindings are absent', async () => {
    isWailsReadyMock.mockReturnValue(false)
    await expect(initSystemNotifications()).resolves.toBeUndefined()
    expect(initGoMock).not.toHaveBeenCalled()
  })

  it('never throws when the Go init rejects', async () => {
    initGoMock.mockRejectedValueOnce(new Error('D-Bus unavailable'))
    await expect(initSystemNotifications()).resolves.toBeUndefined()
    expect(loggerWarnSpy).toHaveBeenCalled()
  })
})
