// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { configMocks, onGlobalEventMock } = vi.hoisted(() => ({
  configMocks: { getSecuritySettings: vi.fn() },
  // Typed to the real onGlobalEvent() signature (event name + handler).
  onGlobalEventMock: vi.fn((_name: string, _handler: () => void) => () => {}),
}))

vi.mock('@/api/config', () => ({ getSecuritySettings: configMocks.getSecuritySettings }))
vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { useAutonomyLoader } from './useAutonomyLoader'
import {
  DEFAULT_AUTONOMY_MODE,
  DEFAULT_SILENT_POLICIES,
  useAutonomyStore,
} from '@/stores/autonomyStore'
import type { SecuritySettingsResponse } from '@/types/models'

/** Minimal GetSecuritySettings response; the hook reads the autonomy posture. */
function makeSettings(
  over: Partial<Pick<SecuritySettingsResponse, 'autonomy_mode' | 'silent_mode'>> = {},
): SecuritySettingsResponse {
  return {
    groups: {},
    auto_approve_workspace_writes: false,
    autonomy_mode: 'standard',
    ...over,
  }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

const capturedHandlers = new Map<string, () => void>()
onGlobalEventMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  return () => {}
})

function Harness(): null {
  useAutonomyLoader()
  return null
}

function renderHook(): void {
  capturedHandlers.clear()
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Let the fire-and-forget promise chain settle past the store writes. */
async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

beforeEach(() => {
  configMocks.getSecuritySettings.mockReset()
  useAutonomyStore.setState({
    autonomy_mode: DEFAULT_AUTONOMY_MODE,
    ...DEFAULT_SILENT_POLICIES,
    loaded: false,
  })
})

afterEach(() => {
  if (root) {
    act(() => {
      root?.unmount()
    })
  }
  root = null
  container?.remove()
  container = null
})

describe('useAutonomyLoader', () => {
  it('hydrates the store from GetSecuritySettings on mount', async () => {
    configMocks.getSecuritySettings.mockResolvedValue(
      makeSettings({
        autonomy_mode: 'silent',
        silent_mode: {
          tool_confirm: { mode: 'deny' },
          step_limit: { mode: 'stop' },
          ask_user: { mode: 'enable' },
          review_prompt: { mode: 'allow' },
        },
      }),
    )
    renderHook()
    await flush()

    const s = useAutonomyStore.getState()
    expect(s.loaded).toBe(true)
    expect(s.autonomy_mode).toBe('silent')
    expect(s.tool_confirm.mode).toBe('deny')
    expect(s.review_prompt.mode).toBe('allow')
  })

  it('falls back to the documented defaults when the response omits silent_mode', async () => {
    configMocks.getSecuritySettings.mockResolvedValue(makeSettings({ autonomy_mode: 'silent' }))
    renderHook()
    await flush()

    const s = useAutonomyStore.getState()
    expect(s.autonomy_mode).toBe('silent')
    expect(s.tool_confirm.mode).toBe('judge')
    expect(s.review_prompt.mode).toBe('suppress')
  })

  it('re-reads on config:updated so a Settings save reaches the gate without a restart', async () => {
    configMocks.getSecuritySettings.mockResolvedValue(makeSettings())
    renderHook()
    await flush()
    expect(configMocks.getSecuritySettings).toHaveBeenCalledTimes(1)

    configMocks.getSecuritySettings.mockResolvedValue(
      makeSettings({
        autonomy_mode: 'silent',
        silent_mode: DEFAULT_SILENT_POLICIES,
      }),
    )
    await act(async () => {
      capturedHandlers.get('config:updated')?.()
    })
    await flush()

    expect(useAutonomyStore.getState().autonomy_mode).toBe('silent')
    // The mount-time startup race is covered by the backend:ready retry.
    expect(capturedHandlers.has('backend:ready')).toBe(true)
  })

  it('fails safe to standard (default, not latched) when the read rejects', async () => {
    configMocks.getSecuritySettings.mockRejectedValue(new Error('boom'))
    renderHook()
    await flush()

    const s = useAutonomyStore.getState()
    expect(s.autonomy_mode).toBe('standard')
    expect(s.review_prompt.mode).toBe('suppress')
    expect(s.loaded).toBe(false)
  })
})
