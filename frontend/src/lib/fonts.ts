/**
 * TS mirror of the typography font-family tokens defined in the `@theme`
 * block of `frontend/src/index.css` (see "Typography — font families").
 *
 * Keep in sync with `@theme` — CSS is the source of truth; these constants
 * exist because some non-CSS consumers (CodeMirror 6 themes and the xterm
 * constructor) need a literal stack string, not a `var()` reference.
 *
 * When changing a stack: edit `@theme` first, then mirror here.
 */

/** Matches `--font-mono` in index.css @theme. */
export const FONT_MONO_STACK =
  'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace'

/** Matches `--font-sans` in index.css @theme. */
export const FONT_SANS_STACK =
  'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, sans-serif'
