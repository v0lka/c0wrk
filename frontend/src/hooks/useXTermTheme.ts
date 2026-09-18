import { useMemo } from 'react'
import type { ITheme } from '@xterm/xterm'
import type { ThemeType } from '@/stores/themeStore'

/**
 * The subset of ITheme keys that we resolve from CSS custom properties.
 * Properties not listed here fall back to XTerm defaults.
 */
type ThemeTokenKey =
  | 'background'
  | 'foreground'
  | 'cursor'
  | 'selectionBackground'
  | 'black'
  | 'red'
  | 'green'
  | 'yellow'
  | 'blue'
  | 'magenta'
  | 'cyan'
  | 'white'
  | 'brightBlack'
  | 'brightRed'
  | 'brightGreen'
  | 'brightYellow'
  | 'brightBlue'
  | 'brightMagenta'
  | 'brightCyan'
  | 'brightWhite'

/**
 * Maps a CSS custom property name to its resolved hex value.
 * Returns an empty string for missing variables.
 */
function getCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * XTerm theme key → CSS custom property mapping.
 * Single source of truth is index.css @theme; this table only defines
 * which token feeds which ANSI slot.
 */
const TOKEN_MAP: Record<ThemeTokenKey, string> = {
  background: '--color-popover',
  foreground: '--color-foreground',
  cursor: '--color-terminal-cursor',
  selectionBackground: '--color-terminal-selection',
  black: '--color-background',
  red: '--color-destructive',
  green: '--color-success',
  yellow: '--color-highlight',
  blue: '--color-info',
  magenta: '--color-hljs-keyword',
  cyan: '--color-hljs-literal',
  white: '--color-foreground',
  brightBlack: '--color-hljs-comment',
  brightRed: '--color-destructive',
  brightGreen: '--color-success',
  brightYellow: '--color-highlight',
  brightBlue: '--color-info',
  brightMagenta: '--color-hljs-keyword',
  brightCyan: '--color-hljs-literal',
  brightWhite: '--color-terminal-bright-white',
}

/**
 * Reads the XTerm theme from CSS custom properties at call time.
 * Only includes keys whose CSS variable resolves to a non-empty value.
 */
function resolveThemeFromCSS(): ITheme {
  const theme: Partial<ITheme> = {}
  for (const [key, varName] of Object.entries(TOKEN_MAP)) {
    const value = getCSSVar(varName)
    if (value) {
      ;(theme as Record<string, string>)[key] = value
    }
  }
  return theme as ITheme
}

/**
 * Hook that returns an XTerm theme object resolved from CSS custom properties.
 * Re-resolves whenever `themeKey` OR `themeType` changes so the terminal
 * follows the active color palette without restarting the session. Pass the
 * active theme's IDENTITY (themeStore's selectActiveThemeId) as `themeKey`
 * and its dark/light TYPE (selectActiveThemeType) as `themeType`: the CSS
 * variables change not only when one same-type custom theme replaces another
 * (the data-custom-theme CSS swap — only the id key distinguishes those) but
 * also when applyThemes corrects the TYPE of an unchanged theme id (an
 * edited theme file flipping color-scheme) — which only the type key catches.
 */
export function useXTermTheme(themeKey: string, themeType: ThemeType): ITheme {
  return useMemo(() => {
    void themeKey
    void themeType
    return resolveThemeFromCSS()
  }, [themeKey, themeType])
}
