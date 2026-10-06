// @vitest-environment jsdom
//
// Tests for the system-font RPC wrappers in api/fonts.ts: boundary
// validation of both backend responses (type guards), name sanitization
// (quotes / control chars / length cap), the unavailable → empty mapping,
// and the log-then-rethrow error path (the api wrapper pattern).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import {
  getSystemFonts,
  listFontFamilies,
  isSystemFontsResponse,
  isFontFamiliesResponse,
  sanitizeFontName,
  MAX_FONT_NAME_LENGTH,
} from './fonts'

// The wrappers report malformed successful responses through logger.error,
// which is console.error at the default level.
let consoleError: ReturnType<typeof vi.spyOn>

/** Install a fake Wails App binding with the given RPC implementations.
 *  RPCs may take arguments (e.g. ListFontFamilies's monospace flag), so the
 *  record accepts any function shape (`never[]` params = contravariance
 *  escape hatch: every concrete RPC signature is assignable to it). */
function installAppBinding(impls: Record<string, (...args: never[]) => unknown>): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: impls },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
})

describe('sanitizeFontName', () => {
  it('passes plain names through unchanged', () => {
    expect(sanitizeFontName('Noto Sans')).toBe('Noto Sans')
    expect(sanitizeFontName('DejaVu Sans Mono')).toBe('DejaVu Sans Mono')
  })

  it('strips double quotes', () => {
    expect(sanitizeFontName('"Cantarell"')).toBe('Cantarell')
    // Quotes can be embedded (a raw GVariant-style description).
    expect(sanitizeFontName('Can"tarell 11')).toBe('Cantarell 11')
  })

  it('strips control characters', () => {
    expect(sanitizeFontName('Can\ttarell\n')).toBe('Cantarell')
    expect(sanitizeFontName('\u0000Noto\u0001 Sans')).toBe('Noto Sans')
    expect(sanitizeFontName('Noto\u007F Sans')).toBe('Noto Sans')
  })

  it('caps the length at MAX_FONT_NAME_LENGTH', () => {
    const long = 'A'.repeat(MAX_FONT_NAME_LENGTH + 50)
    expect(sanitizeFontName(long)).toBe('A'.repeat(MAX_FONT_NAME_LENGTH))
    expect(sanitizeFontName(long)).toHaveLength(MAX_FONT_NAME_LENGTH)
  })

  it('strips before capping, so the cap applies to clean characters', () => {
    const padded = '"' + 'B'.repeat(MAX_FONT_NAME_LENGTH + 10) + '"'
    expect(sanitizeFontName(padded)).toBe('B'.repeat(MAX_FONT_NAME_LENGTH))
  })

  it('returns null for empty and stripped-to-nothing names', () => {
    expect(sanitizeFontName('')).toBeNull()
    expect(sanitizeFontName('""')).toBeNull()
    expect(sanitizeFontName('\t\n\u0000')).toBeNull()
  })
})

describe('isSystemFontsResponse', () => {
  it('accepts a well-formed detected response', () => {
    expect(isSystemFontsResponse({ ui_family: 'Cantarell', mono_family: 'Monospace' })).toBe(true)
  })

  it('accepts the zero value (nothing detected — there is no availability flag)', () => {
    expect(isSystemFontsResponse({ ui_family: '', mono_family: '' })).toBe(true)
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['string', 'Cantarell'],
    ['missing ui_family', { mono_family: 'Monospace' }],
    ['missing mono_family', { ui_family: 'Cantarell' }],
    ['non-string ui_family', { ui_family: 11, mono_family: 'Monospace' }],
    ['non-string mono_family', { ui_family: 'Cantarell', mono_family: null }],
  ])('rejects %s', (_name, value) => {
    expect(isSystemFontsResponse(value)).toBe(false)
  })
})

describe('isFontFamiliesResponse', () => {
  it('accepts a well-formed listing', () => {
    expect(isFontFamiliesResponse({ available: true, families: ['Cantarell', 'Noto Sans'] })).toBe(true)
  })

  it('accepts an unavailable response', () => {
    expect(isFontFamiliesResponse({ available: false, families: [] })).toBe(true)
  })

  it('accepts an empty listing (successful fontless enumeration)', () => {
    expect(isFontFamiliesResponse({ available: true, families: [] })).toBe(true)
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['string', ['Cantarell']],
    ['missing available', { families: [] }],
    ['missing families', { available: true }],
    ['non-boolean available', { available: 'yes', families: [] }],
    ['non-array families', { available: true, families: 'Cantarell' }],
    ['non-string entry', { available: true, families: ['Cantarell', 11] }],
  ])('rejects %s', (_name, value) => {
    expect(isFontFamiliesResponse(value)).toBe(false)
  })
})

describe('getSystemFonts', () => {
  it('returns both families for a detected response', async () => {
    installAppBinding({
      GetSystemFonts: async () => ({ ui_family: 'Cantarell', mono_family: 'Monospace' }),
    })
    await expect(getSystemFonts()).resolves.toEqual({
      uiFamily: 'Cantarell',
      monoFamily: 'Monospace',
    })
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('sanitizes each family independently', async () => {
    installAppBinding({
      GetSystemFonts: async () => ({ ui_family: '"Cantarell 11"', mono_family: 'Mono\tspace' }),
    })
    await expect(getSystemFonts()).resolves.toEqual({
      uiFamily: 'Cantarell 11',
      monoFamily: 'Monospace',
    })
  })

  it('maps empty wire values to null families without logging', async () => {
    // Nothing detected (KDE, Windows, macOS) is a normal outcome — no logs.
    installAppBinding({
      GetSystemFonts: async () => ({ ui_family: '', mono_family: '' }),
    })
    await expect(getSystemFonts()).resolves.toEqual({ uiFamily: null, monoFamily: null })
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('maps a name that sanitizes to nothing to null', async () => {
    installAppBinding({
      GetSystemFonts: async () => ({ ui_family: '""', mono_family: 'Monospace' }),
    })
    await expect(getSystemFonts()).resolves.toEqual({ uiFamily: null, monoFamily: 'Monospace' })
  })

  it('rejects when the Wails runtime is unavailable, after logging', async () => {
    // window.go is not installed — getApp() throws synchronously inside the
    // wrapper's try, so the throw is logged then re-thrown.
    await expect(getSystemFonts()).rejects.toThrow('Wails App bindings are not available')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('logs and re-throws RPC failures', async () => {
    installAppBinding({
      GetSystemFonts: async () => {
        throw new Error('rpc boom')
      },
    })
    await expect(getSystemFonts()).rejects.toThrow('rpc boom')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['null', async () => null],
    ['undefined', async () => undefined],
    ['string', async () => 'Cantarell'],
    ['missing mono_family', async () => ({ ui_family: 'Cantarell' })],
    ['non-string ui_family', async () => ({ ui_family: 11, mono_family: '' })],
  ])('rejects and logs a malformed response (%s)', async (_name, impl) => {
    installAppBinding({ GetSystemFonts: impl })
    await expect(getSystemFonts()).rejects.toThrow(TypeError)
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('exposes the underlying TypeError message for malformed data', async () => {
    installAppBinding({ GetSystemFonts: async () => ({ nope: true }) })
    await expect(getSystemFonts()).rejects.toThrow('getSystemFonts: backend returned malformed data')
  })
})

describe('listFontFamilies', () => {
  it('returns the sanitized family list', async () => {
    installAppBinding({
      ListFontFamilies: async () => ({
        available: true,
        families: ['Cantarell', '"Noto Sans"', 'DejaVu Sans Mono'],
      }),
    })
    await expect(listFontFamilies(false)).resolves.toEqual([
      'Cantarell',
      'Noto Sans',
      'DejaVu Sans Mono',
    ])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('passes the monospace flag through to the binding verbatim', async () => {
    // The backend owns the fc-list `:mono` narrowing; the wrapper only
    // forwards the flag and validates the shape of whatever returns.
    const impl = vi.fn(async (monospace: boolean) => ({
      available: true,
      families: monospace ? ['DejaVu Sans Mono'] : ['Cantarell'],
    }))
    installAppBinding({ ListFontFamilies: impl })
    await expect(listFontFamilies(true)).resolves.toEqual(['DejaVu Sans Mono'])
    await expect(listFontFamilies(false)).resolves.toEqual(['Cantarell'])
    expect(impl).toHaveBeenNthCalledWith(1, true)
    expect(impl).toHaveBeenNthCalledWith(2, false)
  })

  it('maps available=false to an empty list without logging', async () => {
    // No fontconfig (Windows, macOS) is a normal outcome — not an error.
    installAppBinding({
      ListFontFamilies: async () => ({ available: false, families: [] }),
    })
    await expect(listFontFamilies(false)).resolves.toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('maps an empty-but-successful listing to an empty list', async () => {
    installAppBinding({
      ListFontFamilies: async () => ({ available: true, families: [] }),
    })
    await expect(listFontFamilies(false)).resolves.toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('drops entries that sanitize to nothing', async () => {
    installAppBinding({
      ListFontFamilies: async () => ({
        available: true,
        families: ['Cantarell', '', '""', '\u0007'],
      }),
    })
    await expect(listFontFamilies(false)).resolves.toEqual(['Cantarell'])
  })

  it('caps overlong entries at MAX_FONT_NAME_LENGTH', async () => {
    installAppBinding({
      ListFontFamilies: async () => ({
        available: true,
        families: ['A'.repeat(MAX_FONT_NAME_LENGTH + 1)],
      }),
    })
    await expect(listFontFamilies(false)).resolves.toEqual(['A'.repeat(MAX_FONT_NAME_LENGTH)])
  })

  it('rejects when the Wails runtime is unavailable, after logging', async () => {
    await expect(listFontFamilies(false)).rejects.toThrow('Wails App bindings are not available')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('logs and re-throws RPC failures', async () => {
    installAppBinding({
      ListFontFamilies: async () => {
        throw new Error('rpc boom')
      },
    })
    await expect(listFontFamilies(false)).rejects.toThrow('rpc boom')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['null', async () => null],
    ['undefined', async () => undefined],
    ['string', async () => 'Cantarell'],
    ['missing families', async () => ({ available: true })],
    ['non-array families', async () => ({ available: true, families: null })],
    ['non-string entry', async () => ({ available: true, families: [11] })],
  ])('rejects and logs a malformed response (%s)', async (_name, impl) => {
    installAppBinding({ ListFontFamilies: impl })
    await expect(listFontFamilies(false)).rejects.toThrow(TypeError)
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  it('exposes the underlying TypeError message for malformed data', async () => {
    installAppBinding({ ListFontFamilies: async () => ({ nope: true }) })
    await expect(listFontFamilies(false)).rejects.toThrow(
      'listFontFamilies: backend returned malformed data',
    )
  })
})
