// @vitest-environment jsdom
//
// Tests for the Go notification transport wrappers in api/notifications.ts:
// RPC argument marshalling (undefined values dropped from the data map) and
// the notification_clicked subscription boundary (guard-validated payloads
// reach the callback; malformed ones are reported and dropped).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import {
  initSystemNotifications,
  sendSystemNotification,
  showTestNotification,
  onNotificationClicked,
} from './notifications'

type SendBinding = (title: string, body: string, data?: Record<string, string>) => Promise<void>

function installApp(methods: Record<string, unknown>): void {
  ;(window as unknown as Record<string, unknown>).go = { desktop: { App: methods } }
}

let consoleWarn: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
  delete (window as unknown as Record<string, unknown>).runtime
})

afterEach(() => {
  consoleWarn.mockRestore()
})

describe('initSystemNotifications', () => {
  it('calls the InitNotifications binding', async () => {
    const init = vi.fn(async () => {})
    installApp({ InitNotifications: init })
    await initSystemNotifications()
    expect(init).toHaveBeenCalledTimes(1)
  })

  it('propagates RPC failures to the caller (lib downgrades to warn)', async () => {
    installApp({ InitNotifications: async () => { throw new Error('D-Bus unavailable') } })
    await expect(initSystemNotifications()).rejects.toThrow('D-Bus unavailable')
  })

  it('throws when the App bindings are absent (no Wails host)', async () => {
    await expect(initSystemNotifications()).rejects.toThrow('Wails App bindings are not available')
  })
})

describe('sendSystemNotification', () => {
  it('forwards title, body and the data map', async () => {
    const send = vi.fn(async (_t: string, _b: string, _d?: Record<string, string>) => {})
    installApp({ SendSystemNotification: send })
    await sendSystemNotification('T', 'B', { sessionId: 's1', projectId: 'p1' })
    expect(send).toHaveBeenCalledWith('T', 'B', { sessionId: 's1', projectId: 'p1' })
  })

  it('drops undefined entries so map[string]string never sees a null', async () => {
    const send = vi.fn<(title: string, body: string, data?: Record<string, string>) => Promise<void>>(async () => {})
    installApp({ SendNotification: send })
    installApp({ SendSystemNotification: send })
    await sendSystemNotification('T', 'B', { sessionId: 's1', projectId: undefined })
    expect(send).toHaveBeenCalledWith('T', 'B', { sessionId: 's1' })
  })

  it('sends undefined (no data map) when every entry was undefined', async () => {
    const send = vi.fn<SendBinding>(async () => {})
    installApp({ SendSystemNotification: send })
    await sendSystemNotification('T', 'B', { projectId: undefined })
    expect(send).toHaveBeenCalledWith('T', 'B', undefined)
  })
})

describe('showTestNotification', () => {
  it('calls the ShowTestNotification binding', async () => {
    const show = vi.fn(async () => {})
    installApp({ ShowTestNotification: show })
    await showTestNotification()
    expect(show).toHaveBeenCalledTimes(1)
  })
})

describe('onNotificationClicked', () => {
  /** Minimal fake runtime that records EventsOn handlers per event name. */
  function installEventsRuntime(): Record<string, ((...data: unknown[]) => void)[]> {
    const runtimeHandlers: Record<string, ((...data: unknown[]) => void)[]> = {}
    ;(window as unknown as Record<string, unknown>).runtime = {
      EventsOn: (name: string, cb: (...data: unknown[]) => void) => {
        ;(runtimeHandlers[name] ??= []).push(cb)
        return () => {}
      },
    }
    return runtimeHandlers
  }

  it('delivers a valid payload to the callback', () => {
    const runtimeHandlers = installEventsRuntime()
    const got: unknown[] = []
    const off = onNotificationClicked((data) => got.push(data))
    runtimeHandlers['notification_clicked']![0]!({
      notification_id: 'n1',
      session_id: 's1',
      project_id: 'p1',
    })
    expect(got).toEqual([{ notification_id: 'n1', session_id: 's1', project_id: 'p1' }])
    off()
  })

  it('drops and reports a malformed payload', () => {
    const runtimeHandlers = installEventsRuntime()
    const got: unknown[] = []
    const off = onNotificationClicked((data) => got.push(data))
    runtimeHandlers['notification_clicked']![0]!({ notification_id: 42 })
    expect(got).toEqual([])
    expect(consoleWarn).toHaveBeenCalled()
    off()
  })

  it('returns a working unsubscribe (no-op without a runtime)', () => {
    expect(() => onNotificationClicked(() => {})).not.toThrow()
  })
})
