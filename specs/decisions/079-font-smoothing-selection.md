# ADR-079: Per-scope font smoothing (anti-aliasing) selection

## Status

Accepted

## Context

[ADR-078](./078-user-font-selection.md) let the user pick the UI and monospaced font families, but the *rendering* of that text — its anti-aliasing — remained entirely under webview/OS control. Displays, webviews, and personal preference legitimately differ (subpixel AA is tuned for LCD geometry, grayscale suits hi-dpi and screenshots, aliased rendering matters for pixel-font aesthetics and debugging). A webview exposes exactly one CSS-level lever for this: the non-standard `-webkit-font-smoothing` property. Its support varies by engine and platform — WebKit and Blink honor it to differing degrees, some ports defer to OS/fontconfig rendering configuration, and canvas rendering ignores it entirely. Any in-app knob built on it is therefore best-effort by nature.

The two font scopes of ADR-078 are independently meaningful for smoothing too: monospace surfaces (terminal, code viewer, code blocks) have different optimal anti-aliasing than UI text, and mono text is exactly where users look closest.

## Decision

1. **Two independent smoothing knobs** — `uiFontSmoothing` and `monoFontSmoothing` in `fontStore`, persisted under `c0wrk-fonts` **v2** (additive fields; a v1 payload shallow-merges over the `''` defaults, no value migration). The value space is `''` (Not set — the default: nothing is written, the webview default stands), `none`, `grayscale`, `subpixel`.
2. **Delivery through CSS custom properties, one mapping function.** `applyFontsToDocument` writes `--font-smoothing-sans` / `--font-smoothing-mono` inline on `<html>` (removed for Not set), always together with the two family properties — the four-property set moves as one, so a partial apply can never reset an untouched knob. `smoothingToCss` in the store is the single mapping onto `-webkit-font-smoothing` values (`none` / `antialiased` / `subpixel-antialiased`).
3. **index.css scopes the values**: a `:root` rule (deliberately not a second `html {` — the type-scale guard brace-matches the first top-level `html {` to exempt the root font-size) applies the sans mode document-wide via `var(--font-smoothing-sans, auto)`; an explicit monospace-surface rule list — `code`, `pre`, `.xterm`, `.cm-viewer-container` — applies the mono mode via `var(--font-smoothing-mono, auto)`. The body-level chat autocomplete tooltip is a UI-font (sans) surface — it inherits the `:root` sans mode and stays out of the mono list. The `auto` fallback makes an absent variable behave exactly as Not set.
4. **Settings UI**: a select-only combobox per scope (`StringCombobox` gains a generic `editable: false` mode — read-only input, dropdown the sole input, programmatic `input` events ignored), placed in a 2×2 grid directly under its font picker; option labels preview their own mode. Free text is meaningless for an enum and can never reach the store.
5. **Best-effort stance, no OS writes.** c0wrk never writes fontconfig, gsettings, or webview configuration; where the webview ignores the property the knob is a no-op, never a breakage. The terminal is covered because xterm renders through its DOM renderer (no canvas/webgl addon is mounted); smoothing never changes advance widths, so no re-fit or re-measure accompanies a change.

## Consequences

- Users gain per-scope smoothing control with a one-click "Not set" return to stock rendering; `''` ⇒ no property ⇒ rendering identical to a build without the feature.
- Where a webview ignores `-webkit-font-smoothing`, the UI still works and persists a preference that simply has no effect — the spec and the inline documentation state this openly instead of feature-detecting an unobservable capability (CSSOM support cannot be probed reliably in a webview that parses but ignores the property).
- `StringCombobox` grows a second, select-only usage mode; its free-text contract stays the default and unchanged.
- Future rendering-affecting knobs (hinting, ligatures) have a template to follow: store value → inline custom property → scoped index.css rule → preview-styled option labels.

## Alternatives Considered

- **`text-rendering` / `font-smooth`** — different or obsolete properties: `text-rendering` trades metrics/kerning behavior, not AA; `font-smooth` is a legacy Safari alias. Rejected.
- **OS-level configuration** (fontconfig properties, gsettings `font-antialiasing`) — machine-global, affects every application, requires writing user config files from an app; rejected in favor of an app-scoped, user-reversible knob.
- **A single app-wide smoothing knob** — loses the mono/sans distinction ADR-078's two-scope model already established; mono surfaces are where smoothing differences are most visible. Rejected.
- **A second dropdown primitive** (e.g. Radix Select) for the enum — introduces a visually different widget into the same form for no functional gain; the select-only mode of the existing combobox keeps one idiom. Rejected.
