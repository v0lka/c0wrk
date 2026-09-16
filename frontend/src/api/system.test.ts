// @vitest-environment jsdom
//
// Tests for the GetProcessMemory RPC wrapper in api/system.ts: boundary
// validation of the backend response and silent propagation of the
// "runtime unavailable" and "RPC failed" paths (the polling caller treats
// both as "indicator unavailable").

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { getProcessMemory } from './system'

// The wrapper reports malformed successful responses through logger.error,
// which is console.error at the default level.
let consoleError: ReturnType<typeof vi.spyOn>

/** Install a fake Wails App binding with the given GetProcessMemory impl. */
function installAppBinding(impl: () => unknown): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: { GetProcessMemory: impl } },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
})

describe('getProcessMemory', () => {
  it('returns the validated byte count', async () => {
    installAppBinding(async () => 1_234_567_896)
    await expect(getProcessMemory()).resolves.toBe(1_234_567_896)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('returns 0 unchanged (a zero RSS is a valid byte count)', async () => {
    installAppBinding(async () => 0)
    await expect(getProcessMemory()).resolves.toBe(0)
  })

  it('rejects when the Wails runtime is unavailable', async () => {
    // window.go is not installed — getApp() throws synchronously.
    await expect(getProcessMemory()).rejects.toThrow('Wails App bindings are not available')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('propagates RPC failures unlogged (the caller hides silently)', async () => {
    installAppBinding(async () => {
      throw new Error('rpc boom')
    })
    await expect(getProcessMemory()).rejects.toThrow('rpc boom')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each([
    ['string', async () => '1234'],
    ['negative', async () => -1],
    ['NaN', async () => Number.NaN],
    ['null', async () => null],
    ['undefined', async () => undefined],
  ])('rejects and logs a malformed response (%s)', async (_name, impl) => {
    installAppBinding(impl)
    await expect(getProcessMemory()).rejects.toThrow('non-numeric')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })
})
