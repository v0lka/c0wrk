// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { isLinuxHost } from './platform'

// navigator.platform/userAgent are getter-backed in jsdom; defineProperty
// shadows them deterministically per test (the project convention, cf. the
// navigator.platform pins in SystemNotificationSettings.test).
const originalPlatform = navigator.platform
const originalUserAgent = navigator.userAgent

/** Pins both probe strings, runs one assertion, lets afterEach restore. */
function withNavigator(platform: string, userAgent: string, run: () => void): void {
  Object.defineProperty(navigator, 'platform', { configurable: true, value: platform })
  Object.defineProperty(navigator, 'userAgent', { configurable: true, value: userAgent })
  run()
}

afterEach(() => {
  Object.defineProperty(navigator, 'platform', { configurable: true, value: originalPlatform })
  Object.defineProperty(navigator, 'userAgent', { configurable: true, value: originalUserAgent })
})

describe('isLinuxHost', () => {
  it('matches Linux x86_64 via navigator.platform', () => {
    let result = false
    withNavigator('Linux x86_64', 'Mozilla/5.0 (Windows NT 10.0)', () => {
      result = isLinuxHost()
    })
    expect(result).toBe(true)
  })

  it('matches Linux aarch64 — the matcher must stay architecture-agnostic', () => {
    // WebKit composes navigator.platform from uname(): "Linux aarch64" on
    // arm64 — a first-class release platform (ADR-027). The match is on the
    // "Linux" substring, never an exact arch string.
    let result = false
    withNavigator('Linux aarch64', 'Mozilla/5.0 (Windows NT 10.0)', () => {
      result = isLinuxHost()
    })
    expect(result).toBe(true)
  })

  it('rejects Win32', () => {
    let result = true
    withNavigator('Win32', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)', () => {
      result = isLinuxHost()
    })
    expect(result).toBe(false)
  })

  it('rejects MacIntel', () => {
    let result = true
    withNavigator('MacIntel', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', () => {
      result = isLinuxHost()
    })
    expect(result).toBe(false)
  })

  it('falls back to userAgent when platform is empty', () => {
    // Some engines leave navigator.platform empty; the userAgent then carries
    // the platform signal ("X11; Linux x86_64" in the UA string).
    let result = false
    withNavigator(
      '',
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko)',
      () => {
        result = isLinuxHost()
      },
    )
    expect(result).toBe(true)
  })

  it('stays false on an empty platform with a non-Linux userAgent', () => {
    let result = true
    withNavigator('', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)', () => {
      result = isLinuxHost()
    })
    expect(result).toBe(false)
  })

  it('fails closed without a navigator', () => {
    // The typeof guard keeps the helper import-safe in DOM-less
    // environments (e.g. the default node vitest environment): it reports
    // false instead of throwing a ReferenceError.
    vi.stubGlobal('navigator', undefined)
    try {
      expect(isLinuxHost()).toBe(false)
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
