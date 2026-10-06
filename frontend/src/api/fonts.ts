// System font API wrappers.
//
// Wraps the generated Wails bindings for GetSystemFonts and ListFontFamilies
// so components never import wailsjs directly. GetSystemFonts reports the
// desktop environment's UI and monospace font families (GNOME interface and
// monospace fonts via gsettings on Linux); ListFontFamilies enumerates the
// installed families (fontconfig on Linux), optionally narrowed to the
// families fontconfig tags as monospace. Both responses are validated at
// this boundary: malformed data raises a TypeError (never leaks a half-shaped
// record into UI stores), an "unavailable" answer maps to an empty result
// without logging (a normal outcome on KDE, Windows, macOS), and backend
// errors are logged then re-thrown for the caller to surface.
//
// Every font name crossing this boundary is sanitized (see sanitizeFontName):
// the raw values come from OS font configuration and must be safe to embed
// into CSS font stacks and combobox options.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { MAX_FONT_NAME_LENGTH, stripUnsafeFontNameChars } from '@/lib/fonts'

/** Hard cap on any single font name passing through this boundary. Owned by
 *  lib/fonts.ts (the font store canonicalizes against the same number); this
 *  module imports it for sanitizeFontName and re-exports it as part of the
 *  boundary's public surface. */
export { MAX_FONT_NAME_LENGTH }

/** The desktop environment's system font families, reduced to what the UI
 *  needs: the family names only. Point sizes and style tokens stay behind —
 *  c0wrk owns its type scale (14px base + the UI Scale setting) and applies
 *  weight/slant through CSS. A null family means "not detected here" — a
 *  normal outcome on non-GNOME desktops and non-Linux platforms. */
export interface SystemFontFamilies {
  uiFamily: string | null
  monoFamily: string | null
}

/** Wire shape of the backend's SystemFontsResponse (json-tagged fields).
 *  The backend has no availability flag: an empty string is the zero value
 *  for every "not detected" outcome. */
export interface SystemFontsResponseWire {
  ui_family: string
  mono_family: string
}

/** Wire shape of the backend's FontFamiliesResponse (json-tagged fields).
 *  `available: false` means the enumeration mechanism is unavailable (no
 *  fontconfig) — never "zero fonts" (that is `available: true, families:
 *  []`). `families` is never null on the wire. */
export interface FontFamiliesResponseWire {
  available: boolean
  families: string[]
}

/**
 * Sanitize one raw font name from OS font metadata: strip the shared unsafe
 * character set (double quotes, backslashes, control characters — see
 * `stripUnsafeFontNameChars` in lib/fonts), then cap the length. Returns
 * null when nothing usable remains (the name was empty or consisted entirely
 * of stripped characters) — callers treat that the same as "not detected"
 * and drop the entry. The stripping is the same helper the store-side
 * normalizeFamily runs, so a shown picker option and the applied value stay
 * the same name.
 */
export function sanitizeFontName(raw: string): string | null {
  const cleaned = stripUnsafeFontNameChars(raw)
  if (cleaned.length === 0) return null
  return cleaned.slice(0, MAX_FONT_NAME_LENGTH)
}

/**
 * Type guard: a well-formed GetSystemFonts response carries string
 * `ui_family` and `mono_family` fields. Unlike the old SystemUIFontResponse
 * there is no `available` flag — the backend emits empty strings for every
 * "not detected" outcome, so empty values are valid here.
 */
export function isSystemFontsResponse(v: unknown): v is SystemFontsResponseWire {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return typeof o.ui_family === 'string' && typeof o.mono_family === 'string'
}

/**
 * Type guard: a well-formed ListFontFamilies response carries a boolean
 * `available` and a `families` array of strings (`[]` is valid — a
 * successful fontless listing).
 */
export function isFontFamiliesResponse(v: unknown): v is FontFamiliesResponseWire {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  if (typeof o.available !== 'boolean') return false
  if (!Array.isArray(o.families)) return false
  return o.families.every((f) => typeof f === 'string')
}

/**
 * Fetch the desktop environment's UI and monospace font families. Resolves
 * with null per family that was not detected (empty wire value — non-GNOME
 * desktops and non-Linux platforms; a normal outcome, never logged). Throws
 * TypeError on a malformed response and re-throws backend errors after
 * logging them.
 */
export async function getSystemFonts(): Promise<SystemFontFamilies> {
  try {
    const app = getApp()
    const result: unknown = await app.GetSystemFonts()
    if (!isSystemFontsResponse(result)) {
      throw new TypeError('getSystemFonts: backend returned malformed data')
    }
    return {
      uiFamily: sanitizeFontName(result.ui_family),
      monoFamily: sanitizeFontName(result.mono_family),
    }
  } catch (err) {
    logger.error('Failed to get system fonts:', err)
    throw err
  }
}

/**
 * Enumerate the installed font families. With `monospace` the backend
 * narrows the listing to the families fontconfig tags as monospace
 * (`fc-list :mono`) — the monospace picker's data source; `false` enumerates
 * every installed family (the interface picker's). Returns [] when the
 * enumeration mechanism is unavailable (`available=false` — no fontconfig
 * here; a normal outcome, not an error — nothing is logged) and for a
 * successful fontless listing. Names are sanitized; entries that sanitize to
 * nothing are dropped. Throws TypeError on a malformed response and
 * re-throws backend errors after logging them.
 */
export async function listFontFamilies(monospace: boolean): Promise<string[]> {
  try {
    const app = getApp()
    const result: unknown = await app.ListFontFamilies(monospace)
    if (!isFontFamiliesResponse(result)) {
      throw new TypeError('listFontFamilies: backend returned malformed data')
    }
    if (!result.available) return []
    return result.families
      .map(sanitizeFontName)
      .filter((name): name is string => name !== null)
  } catch (err) {
    logger.error('Failed to list font families:', err)
    throw err
  }
}
