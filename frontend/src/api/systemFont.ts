// System UI font API wrappers.
//
// Wraps the generated Wails binding for GetSystemUIFont so components never
// import wailsjs directly. The RPC reports the desktop environment's UI font
// family (GNOME interface font via gsettings on Linux). The response is
// validated at this boundary: malformed data raises a TypeError (never leaks a
// half-shaped record into UI stores), an `available=false` answer maps to
// null ("the setting does not exist here" — a normal outcome on KDE, Windows,
// macOS), and backend errors are logged then re-thrown for the caller to
// surface.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

/** The detected desktop-environment UI font, reduced to what the UI needs:
 *  the family name only. The point size and style tokens stay behind — c0wrk
 *  owns its type scale (14px base + the UI Scale setting) and applies
 *  weight/slant through CSS. */
export interface SystemUIFont {
  family: string
}

/** Wire shape of the backend's SystemUIFontResponse (json-tagged fields). */
export interface SystemUIFontResponseWire {
  available: boolean
  font_family: string
}

/**
 * Type guard: a well-formed GetSystemUIFont response carries a boolean
 * `available` and a string `font_family`. A non-empty family is required only
 * when `available` is true — the backend emits the zero value (`available:
 * false, font_family: ""`) for every "not detected" outcome, and an empty
 * family alongside `available: true` would be schema drift (the parser never
 * returns an empty ok), not a legit font name.
 */
export function isSystemUIFontResponse(v: unknown): v is SystemUIFontResponseWire {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  if (typeof o.available !== 'boolean') return false
  if (typeof o.font_family !== 'string') return false
  if (o.available && o.font_family.length === 0) return false
  return true
}

/**
 * Fetch the desktop environment's UI font family. Returns null when no system
 * font was detected (`available=false` — non-GNOME desktops and non-Linux
 * platforms). Throws TypeError on a malformed response and re-throws backend
 * errors after logging them.
 */
export async function getSystemUIFont(): Promise<SystemUIFont | null> {
  try {
    const app = getApp()
    const result: unknown = await app.GetSystemUIFont()
    if (!isSystemUIFontResponse(result)) {
      throw new TypeError('getSystemUIFont: backend returned malformed data')
    }
    if (!result.available) return null
    return { family: result.font_family }
  } catch (err) {
    logger.error('Failed to get system UI font:', err)
    throw err
  }
}
