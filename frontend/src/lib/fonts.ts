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

/**
 * The one character set every font-name normalization boundary strips:
 * double quotes and backslashes (the name is interpolated into a
 * double-quoted CSS token, and a family name legitimately never carries
 * either), then control characters (OS metadata, pasted or hand-edited
 * input must never carry them into a CSS font stack or UI state). Shared by
 * all three boundaries — `api/fonts.sanitizeFontName` (backend data),
 * `fontStore.normalizeFamily` (store + apply) and
 * `FontSettings.normalizeTyped` (typed input) — so the "same normalization"
 * property holds by construction, not by three copies staying in sync.
 */
export function stripUnsafeFontNameChars(raw: string): string {
  // Deliberate control-character regex: stripping control characters out of
  // untrusted font names is this sanitizer's whole job — they must never
  // reach a CSS font stack or UI state.
  // eslint-disable-next-line no-control-regex
  return raw.replace(/["\\]/g, '').replace(/[\u0000-\u001F\u007F]/g, '')
}

/**
 * The bundled icon font family — the exact `@theme` family of `--font-icon`
 * in `frontend/src/index.css` (SauceCodePro NF, the Nerd Font webfont served
 * from `src/assets/fonts`). Invisible to fontconfig (`fc-list` cannot see
 * it), yet always resolvable in this document. It LEADS `FONT_MONO_STACK`
 * (and the `--font-mono` token it mirrors), so every monospace surface —
 * the terminal, code blocks, the CodeMirror viewer — resolves Nerd Font
 * glyphs (starship, eza, git status decorations) by default, and a
 * user-picked mono family composed in front of the stack still renders the
 * text while this entry only catches the glyphs it lacks (per-glyph
 * fallback). Keep in sync with the `--font-icon` token.
 */
export const FONT_ICON_FAMILY = 'SauceCodePro NF'

/**
 * Matches `--font-mono` in index.css @theme. Leads with FONT_ICON_FAMILY
 * (see its doc): the bundled Nerd Font family is the uniform first entry of
 * every monospace stack — the default mono rendering app-wide, and the
 * glyph-fallback layer under a user-picked family (composeFontFamily puts
 * the pick in front of this stack).
 */
export const FONT_MONO_STACK = `"${FONT_ICON_FAMILY}", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace`

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
