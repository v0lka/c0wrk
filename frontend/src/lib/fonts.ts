/**
 * TS mirror of the typography font-family tokens defined in the `@theme`
 * block of `frontend/src/index.css` (see "Typography — font families").
 *
 * Keep in sync with `@theme` — CSS is the source of truth; these constants
 * exist because non-CSS consumers need a literal stack string, not a `var()`
 * reference (the xterm constructor, which measures glyphs on canvas where
 * `var()` does not resolve — CodeMirror themes read the CSS vars directly).
 *
 * When changing a stack: edit `@theme` first, then mirror here.
 */

/** Hard cap on any single font name crossing the font boundary: api/fonts
 *  sanitizes against it and the font store canonicalizes against it, so one
 *  number owns the limit. OS font metadata is trusted to be short; a longer
 *  value is treated as corrupted metadata and truncated rather than forwarded
 *  into CSS or UI state. */
export const MAX_FONT_NAME_LENGTH = 100

/** Matches `--font-mono` in index.css @theme. */
export const FONT_MONO_STACK =
  'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace'

/** Matches `--font-sans` in index.css @theme. */
export const FONT_SANS_STACK =
  'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, sans-serif'

/**
 * Composes a full CSS `font-family` value from a user-chosen family name:
 * the family as one double-quoted CSS token (multi-word names must not
 * split) prepended to the stock stack — so a missing user font degrades to
 * the stock stack, never to a bare serif default — or the bare default
 * stack when the family is `null` (c0wrk's default). Call form: consumers
 * pass the result to options that the type-scale guard scans
 * (`fontFamily:` must not carry a literal/template there).
 */
export function composeFontFamily(family: string | null, stack: string): string {
  return family === null ? stack : `"${family}", ${stack}`
}
