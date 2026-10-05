// @vitest-environment jsdom
//
// Tests for the GetSystemUIFont RPC wrapper in api/systemFont.ts: boundary
// validation of the backend response (type guard), the available=false →
// null mapping, and the log-then-rethrow error path (api/themes.ts pattern).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { getSystemUIFont, isSystemUIFontResponse } from './systemFont'

// The wrapper reports malformed successful responses through logger.error,
// which is console.error at the default level.
let consoleError: ReturnType<typeof vi.spyOn>

/** Install a fake Wails App binding with the given GetSystemUIFont impl. */
function installAppBinding(impl: () => unknown): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: { GetSystemUIFont: impl } },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
})

describe('isSystemUIFontResponse', () => {
  it('accepts a well-formed detected response', () => {
    expect(isSystemUIFontResponse({ available: true, font_family: 'Noto Sans' })).toBe(true)
  })

  it('accepts the zero value (not detected)', () => {
    expect(isSystemUIFontResponse({ available: false, font_family: '' })).toBe(true)
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['string', 'Noto Sans'],
    ['missing available', { font_family: 'Noto Sans' }],
    ['missing font_family', { available: false }],
    ['non-boolean available', { available: 'yes', font_family: 'Noto Sans' }],
    ['non-string font_family', { available: true, font_family: 11 }],
    ['empty family with available=true', { available: true, font_family: '' }],
  ])('rejects %s', (_name, value) => {
    expect(isSystemUIFontResponse(value)).toBe(false)
  })
})

describe('getSystemUIFont', () => {
  it('returns {family} for a detected font', async () => {
    installAppBinding(async () => ({ available: true, font_family: 'Noto Sans' }))
    await expect(getSystemUIFont()).resolves.toEqual({ family: 'Noto Sans' })
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('keeps multi-word family names intact', async () => {
    installAppBinding(async () => ({ available: true, font_family: 'DejaVu Sans' }))
    await expect(getSystemUIFont()).resolves.toEqual({ family: 'DejaVu Sans' })
  })

  it('maps available=false to null', async () => {
    // A desktop without a detectable UI font (KDE, Windows, macOS) is a
    // normal outcome, not an error — nothing is logged.
    installAppBinding(async () => ({ available: false, font_family: '' }))
    await expect(getSystemUIFont()).resolves.toBeNull()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('rejects when the Wails runtime is unavailable, after logging', async () => {
    // window.go is not installed — getApp() throws synchronously inside the
    // wrapper's try, so the throw is logged then re-thrown.
    await expect(getSystemUIFont()).rejects.toThrow('Wails App bindings are not available')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('logs and re-throws RPC failures', async () => {
    installAppBinding(async () => {
      throw new Error('rpc boom')
    })
    await expect(getSystemUIFont()).rejects.toThrow('rpc boom')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['null', async () => null],
    ['undefined', async () => undefined],
    ['string', async () => 'Noto Sans'],
    ['empty family with available=true', async () => ({ available: true, font_family: '' })],
    ['non-boolean available', async () => ({ available: 1, font_family: 'Noto Sans' })],
  ])('rejects and logs a malformed response (%s)', async (_name, impl) => {
    installAppBinding(impl)
    await expect(getSystemUIFont()).rejects.toThrow(TypeError)
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('exposes the underlying TypeError message for malformed data', async () => {
    installAppBinding(async () => ({ nope: true }))
    await expect(getSystemUIFont()).rejects.toThrow('getSystemUIFont: backend returned malformed data')
  })
})
