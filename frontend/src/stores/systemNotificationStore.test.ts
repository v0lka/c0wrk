// Unit tests for the system-notification master-toggle store.
//
// `soundStore` has no dedicated test file, but its persistence contract (key,
// default, partialized shape) is part of this step's acceptance criteria, so
// this mirrors the persistence assertions of uiScaleStore.test.ts /
// themeStore.test.ts to pin the same contract for the notification store.

// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before the store module is imported so the store works.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

import { useSystemNotificationStore } from '@/stores/systemNotificationStore'

describe('systemNotificationStore', () => {
  beforeEach(() => {
    // Reset to default and clear any persisted state between tests.
    useSystemNotificationStore.setState({ enabled: true })
    localStorage.clear()
  })

  it('defaults to enabled (opt-out, mirroring the sound pipeline)', () => {
    expect(useSystemNotificationStore.getState().enabled).toBe(true)
  })

  it('setEnabled flips the toggle', () => {
    useSystemNotificationStore.getState().setEnabled(false)
    expect(useSystemNotificationStore.getState().enabled).toBe(false)
    useSystemNotificationStore.getState().setEnabled(true)
    expect(useSystemNotificationStore.getState().enabled).toBe(true)
  })

  it('toggle inverts the current value', () => {
    expect(useSystemNotificationStore.getState().enabled).toBe(true)
    useSystemNotificationStore.getState().toggle()
    expect(useSystemNotificationStore.getState().enabled).toBe(false)
    useSystemNotificationStore.getState().toggle()
    expect(useSystemNotificationStore.getState().enabled).toBe(true)
  })

  it('persists only {enabled} under the c0wrk-system-notifications key', () => {
    useSystemNotificationStore.getState().setEnabled(false)
    const raw = localStorage.getItem('c0wrk-system-notifications')
    expect(raw).not.toBeNull()
    // zustand's persist middleware wraps the payload as {state, version};
    // partialize must keep `state` limited to the enabled field (no actions).
    const parsed = JSON.parse(raw as string) as {
      state: Record<string, unknown>
      version: number
    }
    expect(parsed.state).toEqual({ enabled: false })
    expect(parsed.version).toBe(1)
  })
})
