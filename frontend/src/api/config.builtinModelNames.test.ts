// @vitest-environment jsdom
//
// Tests for the GetBuiltinModelNames RPC wrapper in api/config.ts: boundary
// validation of the backend response (an array of non-empty strings — the
// sorted model names of the sp4rk built-in registry).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { getBuiltinModelNames } from './config'

let consoleError: ReturnType<typeof vi.spyOn>

function installAppBinding(impl: () => unknown): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: { GetBuiltinModelNames: impl } },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
})

describe('getBuiltinModelNames', () => {
  it('returns the model-name array', async () => {
    installAppBinding(async () => ['claude-3.5-sonnet', 'gpt-4o', 'gemini-2.5-pro'])
    await expect(getBuiltinModelNames()).resolves.toEqual([
      'claude-3.5-sonnet',
      'gpt-4o',
      'gemini-2.5-pro',
    ])
    // Success must not log a diagnostic.
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each([
    { name: 'null', value: null },
    { name: 'undefined', value: undefined },
    { name: 'non-array', value: 'gpt-4o' },
    { name: 'array with a non-string', value: ['gpt-4o', 42] },
    { name: 'array with an empty string', value: ['gpt-4o', ''] },
  ])('rejects a malformed response: $name', async ({ value }) => {
    installAppBinding(async () => value)
    await expect(getBuiltinModelNames()).rejects.toThrow(/invalid data/)
    // The failure is intercepted at its source: the wrapper logs exactly one
    // error naming the RPC (the caller still sees the rejection).
    expect(consoleError).toHaveBeenCalledTimes(1)
    expect(String(consoleError.mock.calls[0]?.[0])).toContain('built-in model names')
  })

  it('propagates a backend error', async () => {
    installAppBinding(async () => {
      throw new Error('registry unavailable')
    })
    await expect(getBuiltinModelNames()).rejects.toThrow(/registry unavailable/)
    expect(consoleError).toHaveBeenCalledTimes(1)
    expect(String(consoleError.mock.calls[0]?.[0])).toContain('built-in model names')
  })

  it('propagates the runtime-unavailable error when the Wails binding is absent', async () => {
    await expect(getBuiltinModelNames()).rejects.toThrow()
    expect(consoleError).toHaveBeenCalledTimes(1)
  })
})
