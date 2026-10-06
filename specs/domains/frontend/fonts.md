# User Font Selection (UI + Monospace Families and Smoothing)

## Role

Defines the Appearance-tab **Fonts** settings: the user picks the app's UI (sans) and monospaced font families — typed freely, chosen from the session's system-font detection, or picked from the families installed on the host — plus a font-smoothing (anti-aliasing) mode for each scope, and the exact mechanism that carries the choices into the WebKitGTK webview as `@theme` token overrides and `-webkit-font-smoothing` variables ([ADR-076](../../decisions/076-user-font-selection.md), [ADR-077](../../decisions/077-font-smoothing-selection.md)); the desktop's own configuration survives as *candidate discovery* feeding a `Use system` shortcut, not as a mode.

## Key Files

- `backend/frontend_api_system.go` — the `GetSystemFonts` RPC (`SystemFontsResponse{UIFamily, MonoFamily}`) and the `ListFontFamilies(monospace bool)` RPC (`FontFamiliesResponse{Available, Families}`); every "not detected / not available" outcome is the zero response plus a Debug log, never an error
- `backend/systemfont.go` — platform-neutral `parseGnomeFontName` (strips GVariant quoting, the trailing point size, trailing Pango style keywords); shared by both families
- `backend/systemfont_linux.go` / `backend/systemfont_other.go` — desktop detection reader: Linux reads `org.gnome.desktop.interface` `font-name` + `monospace-font-name` via `gsettings` (3 s per key); `!linux` stub
- `backend/fontlist.go` + `backend/fontlist_linux.go` / `backend/fontlist_other.go` — installed-family enumeration: `fc-list [:mono] --format '%{family}\n'` (5 s; the `monospace` flag selects the `:mono` narrowing) → `parseFontFamilies` (comma-alias split, case-insensitive keep-first dedupe, case-insensitive sort); `!linux` stub — detailed in [../../domains/fonts.md](../../domains/fonts.md)
- `backend/frontend_api.go` — the `readSystemFontsFn` / `listFontFamiliesFn` test seams on `FrontendAPI` (nil in production → the platform readers run), mirroring `readProcessRSSFn`
- `frontend/src/api/fonts.ts` — RPC wrappers with boundary validation (`isSystemFontsResponse` / `isFontFamiliesResponse` type guards) and `sanitizeFontName` (strip quotes/control chars, `MAX_FONT_NAME_LENGTH` cap); "not detected" maps to `null` / `[]` without logging
- `frontend/src/hooks/useSystemFonts.ts` — App-root detector: fetch on mount, retry on `backend:ready`, latch on any definitive answer; feeds `setDetectedFonts` only
- `frontend/src/stores/fontStore.ts` — the store: persisted `uiFontFamily`/`monoFontFamily`/`uiFontSmoothing`/`monoFontSmoothing` + session `detectedUIFamily`/`detectedMonoFamily`, `applyFontsToDocument`, `normalizeFamily`, `smoothingToCss`, `removeOrphanSystemFontKey`; persistence under `c0wrk-fonts`
- `frontend/src/lib/fonts.ts` — `FONT_SANS_STACK` / `FONT_MONO_STACK` (mirrors of the index.css `@theme` stacks) and `composeFontFamily(family, stack)` (double-quoted family prepended to the stock stack)
- `frontend/src/main.tsx` — pre-paint `applyFontsToDocument` re-apply + the one-shot `removeOrphanSystemFontKey` retirement of the replaced store's key
- `frontend/src/App.tsx` — mounts `useSystemFonts()` once at the app root (works in every app phase)
- `frontend/src/components/settings/FontSettings.tsx` — the Appearance-tab block: four `StringCombobox`es (a family picker and a smoothing picker per scope, one 2×2 grid) + the `Use system` button
- `frontend/src/components/settings/SettingsModal.tsx` — mounts `FontSettings` on the Appearance tab
- `frontend/src/components/terminal/Terminal.tsx` — consumes the mono choice live (`composeFontFamily` into the xterm constructor at mount; `term.options.fontFamily` + re-fit on change)
- `frontend/src/lib/cmTheme.ts` / `frontend/src/lib/cmChatTheme.ts` — the CodeMirror themes resolve their content font through `var(--font-mono)` / `var(--font-sans)` (no baked stack)
- `frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx`, `frontend/src/components/fileViewer/MiniCodeMirrorField.tsx`, `frontend/src/hooks/useChatEditor.ts` — CM hosts; reconfigure the theme compartment on the font choice to force a height re-measure
- `frontend/src/index.css` — the `@theme` block defining `--font-sans`/`--font-mono`, and the font-smoothing rules consuming the store's `--font-smoothing-*` variables (see [CSS delivery](#css-delivery-theme-token-override) and [Font smoothing](#font-smoothing-anti-aliasing))

## Behavior

### Detection RPC contract

`GetSystemFonts() SystemFontsResponse` on `*backend.FrontendAPI` (wire shape `{ui_family: string, mono_family: string}`):

- An **empty string is "not detected"** — there is no availability flag. Every undetectable-desktop outcome (non-Linux stub, no `gsettings` in PATH, schema absent on KDE/bare WM, reader error) is the zero response `{ui_family: "", mono_family: ""}`; unavailability is a normal answer, not an RPC failure.
- On Linux the reader execs `gsettings get org.gnome.desktop.interface <font-name|monospace-font-name>` (compile-time constant argv) under a 3 s per-key timeout, so a wedged settings daemon can never hang the RPC. Both keys live in one schema — a missing binary/schema takes both down together. The value is read once per app launch.
- Each raw Pango description (`'Noto Sans 11'`, `'DejaVu Sans Mono Bold 10'`) is parsed **independently** by `parseGnomeFontName`: strips single or double GVariant quoting, then trailing size tokens (plain decimal, at most one dot — `strconv.ParseFloat` is avoided so `1e3`/`+11` are never over-stripped), then Pango style keywords (`Bold`/`Italic`/`Oblique`/`Light`/`Regular`/`Medium`/`Heavy`/`Thin`, only while a family token remains below). An empty survivor empties **only its own family** — the other family still crosses the wire.
- A reader error is logged at Debug only (nil-guarded logger). A desktop-side font change is picked up on the next launch — there is no live propagation into a running WebKitGTK instance; the UI says so.

### Installed-family enumeration

`ListFontFamilies(monospace bool) FontFamiliesResponse` (wire shape `{available: boolean, families: string[]}`): `monospace=false` enumerates every installed family (the interface picker's data source), `monospace=true` narrows the listing to the families fontconfig tags as monospace — the `fc-list :mono` pattern, i.e. spacing=mono families; dual-width and proportional families are excluded by fontconfig itself, and the narrowed list is always a subset of the full listing. `available` describes the **mechanism**, not the result — a successful `fc-list` run on a fontless system is `available: true` with `[]`; every reader error (non-Linux stub, no fc-list, probe failure/timeout) is `{available: false, families: []}`. Aliases are split on commas, trimmed, deduplicated case-insensitively keeping the first spelling, sorted case-insensitively; `families` is never `null`. The full pipeline is specified in [../../domains/fonts.md](../../domains/fonts.md).

### Frontend boundary

`frontend/src/api/fonts.ts` is the only path to the RPCs (components never import `wailsjs` directly):

- Type guards validate the wire; malformed data raises `TypeError` (nothing half-shaped enters stores).
- `sanitizeFontName` strips double quotes and control characters and caps at `MAX_FONT_NAME_LENGTH` (100) — the raw values come from OS font metadata and must be safe to embed into CSS font stacks and combobox options; entries that sanitize to nothing are dropped.
- `getSystemFonts` maps an empty wire family to `null`; `listFontFamilies(monospace)` forwards the flag verbatim and maps `available=false` to `[]` — both without logging (normal outcomes). Backend/runtime errors are logged via `logger.error` and re-thrown.

### Detection wiring

`useSystemFonts` (mounted once in `App.tsx`) fetches on mount and retries on the global `backend:ready` event — the mount request can arrive before the backend is ready (splash phase). A failure never latches (the one-shot `backend:ready` emission is never consumed by a doomed attempt); a `backend:ready` arriving while a fetch is in flight is remembered and replayed once it settles; **any** successful answer latches for the effect's lifetime — a `null` detection (empty gsettings values) is definitive, retrying it cannot change the outcome. Success feeds `fontStore.setDetectedFonts(ui, mono)` — one atomic `set`, one store notification — and **never touches `<html>`**: what renders is decided solely by the persisted chosen families, so a late detection cannot repaint text.

### Store and persistence

`fontStore` (persistence under `c0wrk-fonts`, `version: 2`, identity `migrate` — v2 appended the smoothing fields, so a v1 payload shallow-merges over the `''` defaults and no value migration runs):

- `uiFontFamily` / `monoFontFamily: string | null`, `uiFontSmoothing` / `monoFontSmoothing: FontSmoothingSetting` (`''` = Not set) — the user's choices; the **only persisted fields** (`partialize`). `null` = c0wrk's default stack, left untouched on `<html>`; `''` = the webview's own smoothing, no property written.
- `detectedUIFamily` / `detectedMonoFamily: string | null` — the session detections; never persisted (a stale detection would survive a system-side font change) and never applied by themselves — they are candidates the settings offer.
- Every family action calls `applyFontsToDocument` **before** `set` — the effect on `<html>` is immediate; there is no apply/save step anywhere in the UI.
- `normalizeFamily` canonicalizes before storage: quotes and backslashes stripped (the value is interpolated into a double-quoted CSS token; a family name legitimately never carries either), trimmed, empty → back to `null`.
- `main.tsx` re-applies both choices pre-paint (before React renders) for FOUC-free startup, and calls `removeOrphanSystemFontKey` once: the replaced follow-system-font store's boolean payload under `c0wrk-follow-system-font` has no successor in the chosen-family model, so the key is retired, not migrated.

### CSS delivery (`@theme` token override)

`applyFontsToDocument(ui, mono)` writes the inline custom properties `--font-sans` / `--font-mono` on `document.documentElement` — the **exact `@theme` token names** of `frontend/src/index.css` (`--font-sans` feeds the Tailwind `font-sans` utility and the preflight `html` base; `--font-mono` feeds `font-mono`). An inline declaration on `<html>` beats the `:root` theme rule for the same element in the cascade, so **no `index.css` change is involved**. Each value is `composeFontFamily(family, stack)`: the family as one double-quoted CSS token (multi-word names stay one token) prepended to the stock stack — a missing user font degrades to the stock stack, never to a bare serif default. A `null` family `removeProperty`s the override and the stock stack returns byte-identical. The icon font (`--font-icon`, SauceCodePro NF) is outside the mechanism. No-op without `document`; idempotent.

Only the family is carried — never a size or weight: c0wrk owns its type scale (14px base + the UI Scale setting, see [ui-scale.md](ui-scale.md)) and applies weight/slant through CSS.

### Font smoothing (anti-aliasing)

Each scope carries a smoothing knob over the non-standard `-webkit-font-smoothing` property — the only CSS-level anti-aliasing lever a webview exposes; a webview that ignores it renders the knob as a no-op, never a breakage. `FontSmoothingSetting` is `''` (Not set — the webview default stands, nothing is written), `none` (aliased edges), `grayscale`, or `subpixel`; `smoothingToCss` is the single mapping onto the CSS values (`none` / `antialiased` / `subpixel-antialiased`), and unrecognized persisted values collapse to Not set.

- `applyFontsToDocument` writes the modes as the inline custom properties `--font-smoothing-sans` / `--font-smoothing-mono` on `<html>` — removed for Not set — always together with the two family properties: the four-property set is written or removed on every call, so a partial apply can never reset an untouched knob.
- index.css consumes them: a `:root` rule carries `-webkit-font-smoothing: var(--font-smoothing-sans, auto)` document-wide, and a monospace-surface rule list — `code`, `pre`, `.xterm`, `.cm-viewer-container` — overrides with `var(--font-smoothing-mono, auto)`. The chat autocomplete tooltip is a sans (UI-font) surface: it has no rule of its own and inherits the `:root` sans mode. The `:root` selector is deliberate: the type-scale guard brace-matches the **first** top-level `html {` to exempt the root font-size, and an earlier `html {` would swallow that exemption. An absent variable resolves to the `auto` fallback, which is exactly Not set.
- The scopes are independent: the mono mode reaches only the monospace surfaces — inline code and code blocks (preflight/prose mono), the CodeMirror viewer and mini fields, the terminal's DOM-rendered text; the chat input (`.cm-chat-container`, a sans surface) and its body-level autocomplete tooltip follow only the sans knob.
- The terminal renders through xterm's DOM renderer (no canvas/webgl addon is mounted), so the mono knob reaches its text. Smoothing changes glyph rendering only, never advance widths — no re-fit or re-measure accompanies a smoothing change.

### Settings UI

`FontSettings` renders on the Appearance tab: a "Fonts" header, a **`Use system`** outline button, four `StringCombobox`es in one 2×2 grid — *Interface font* / *Monospace font* on the first row, *Interface smoothing* / *Monospace smoothing* directly under them — over `fontStore`, plus a muted caption. Mechanics:

- The family comboboxes are **free-text**: offer order is `Default` (the `null` sentinel, mapped back before it reaches the store), the session-detected family, the current custom value (always present so the choice stays visible/re-pickable), then the installed families. Typed input is normalized like the store (`normalizeTyped`) so a nothing-left input reverts inside the combobox instead of round-tripping a value the store would collapse to `null`.
- The smoothing comboboxes are **select-only** (`StringCombobox` `editable: false` — a read-only input whose dropdown is the sole input; even a synthetic/autofill `input` event is ignored in this mode): the options are exactly `Not set`, `None (aliased)`, `Grayscale`, `Subpixel (LCD)` — labels mapped to/from the store's `FontSmoothingSetting` inside the component, since free text is meaningless for an enum.
- Each smoothing option label is styled with its own `-webkit-font-smoothing` value through `itemStyle` — the same previews-itself contract as the font pickers; `Not set` carries no style, because it stands for "nothing written".
- The two pickers enumerate independently and fail-soft: the interface combobox loads every installed family (`listFontFamilies(false)`), the monospace combobox loads only the fontconfig-mono families (`listFontFamilies(true)`) — a mono-only failure must not empty the interface list. A detection or current value outside the enumeration is still offered.
- **Preview**: every option label — and the current value in the closed field — renders in its own typeface, through `StringCombobox`'s `itemStyle`/`inputStyle` hooks fed by `composeFontFamily`: `Default` previews the stock stack it stands for; a family name is prepended to its picker's stock stack (sans for the interface picker, mono for the monospace picker, so a missing family degrades within its picker's context). No raw `fontFamily` literal appears in component code — the call form the type-scale guard requires.
- The block **always renders** — choosing a family works on every OS; where detection/enumeration come up empty the dropdown simply offers Default (+ current) and typed input carries the rest.
- `Use system` applies what was detected — both families when the desktop reported both, the one it did otherwise — and stays disabled until at least one family is detected (it has nothing to do without a detection).
- The caption lists what was found (`interface: X, monospace: Y`) or reports `System fonts not detected on this desktop.`; a second muted line states that desktop-side font changes apply after restarting c0wrk.
- `listFontFamilies` runs twice per mount (once per flag); a failure (already logged at the api boundary) degrades that picker to the reduced option set — the block keeps working, nothing is surfaced.

### Terminal consumption

`Terminal.tsx` subscribes to `monoFontFamily` (primitive, referentially stable) and derives the xterm font with the same `composeFontFamily` — bare `FONT_MONO_STACK` with `null`, `"Family", <stack>` with a choice. The constructor reads a render-phase ref (a font switch never restarts the session); a live effect assigns only `term.options.fontFamily` and the fit is re-scheduled, because the new family changes cell metrics while the container box is unchanged.

### CodeMirror consumption

All CodeMirror surfaces read the font from the CSS custom properties, not from a baked stack: `createOneDarkCMTheme` styles `.cm-content` with `font-family: var(--font-mono)` (the file viewer's code/markdown-source view and every `MiniCodeMirrorField` — plan fields and other editable mini-fields), and `createChatEditorTheme` styles the chat input's `.cm-scroller` with `font-family: var(--font-sans)`. The fonts therefore ride the same cascade as every other DOM element — the inline override on `<html>` moves them the moment the store commits, with no per-surface plumbing and no theme rebuild.

The chat input's autocomplete tooltip (the `/` skill, `@` file and `#` agent suggestions) is UI chrome for the same editor — and the one surface where a global stylesheet rule cannot win. `@codemirror/autocomplete`'s baseTheme styles the same elements with editor-scoped selectors (`.ͼbase .cm-tooltip.cm-tooltip-autocomplete > ul`, specificity (0,3,1)+) that beat anything in index.css; a global rule there is dead code and the hints silently render in CM's own literal `monospace` family, ignoring both picks. The tooltip's surface rules therefore live in `createChatEditorTheme` (`cmChatTheme.ts`): CodeMirror mirrors the chat editor's theme class onto the tooltip's body-level wrapper (tooltips({parent})), so theme entries tie baseTheme on specificity and win on mount order — the same channel that overrides the base `.cm-scroller` font. The list carries `font-family: var(--font-sans)`, `font-size: var(--text-xs)` and a 240px cap; the container rides the popover skin; rows and the selected row carry the popover colors; matched-text/detail accents follow. index.css keeps only the uncontested accents (skill-item weight/opacity, the nerd-font item icons on `--font-icon`). The live cascade winner — theme over baseTheme — is asserted by `frontend/src/test/cmTooltipCascade.test.ts`, which resolves (specificity, document order) over every rule matching the mounted tooltip; `cmChatTheme.test.ts` pins the mounted rule text.

Because CodeMirror measures line heights lazily, each host still reconfigures its theme compartment when its choice changes (`CodeMirrorFileViewer`/`MiniCodeMirrorField` key the reconfigure on `monoFontFamily`, `useChatEditor` on `uiFontFamily`): the reconfigure forces a redraw so the new metrics are measured before they are used. The theme factories take no font parameter — the reconfigure exists purely as a measurement flush, and the var carries the value.

The xterm terminal stays the one literal-stack consumer: it measures glyphs on canvas, where `var()` does not resolve (see above).

## Boundaries

- **Families only.** No point size, no style tokens cross the boundary — in either direction.
- **Smoothing is best-effort by nature.** `-webkit-font-smoothing` is the only in-app lever; a webview that defers to the OS/fontconfig rendering configuration renders the knob as a no-op. c0wrk never writes fontconfig, gsettings, or webview configuration.
- **Detection/enumeration are Linux probes; the feature is not.** GNOME/gsettings and fontconfig are the only readers implemented; every other platform reports "not detected / unavailable" while the settings remain fully usable (typed + Default).
- **Restart to pick up desktop changes.** Detection is read once per launch; the UI says so.
- **Legacy key retired, not migrated.** A `c0wrk-follow-system-font` user returns to the default typeface — a boolean maps to no family ([ADR-076](../../decisions/076-user-font-selection.md)).
- **No live repaint from detection.** Detections are stored candidates only; `<html>` moves exclusively through the chosen-family actions.

## Error Handling

- **Probe unavailable/error (backend)** → zero/empty response + Debug log (nil-guarded); the UI degrades (no detections → `Use system` disabled; no enumeration → reduced dropdown). Never an RPC error, never user-visible noise.
- **Unparsable gsettings value** → that family's wire field is empty; the other family is unaffected.
- **Malformed wire data (frontend)** → `TypeError` from the type guards; nothing half-shaped enters stores.
- **Unsanitary font name** → `sanitizeFontName` strips quotes/control characters and caps length; empty survivors are dropped.
- **Unrecognized persisted smoothing** → collapsed to Not set on the next apply; nothing arbitrary ever reaches a CSS value.
- **RPC/runtime failure** → logged at the `api/fonts` boundary and re-thrown; `useSystemFonts` keeps the `backend:ready` retry path armed (no latch on failure) and the document keeps whatever it already had; the settings dropdown degrades to Default + current.
- **No DOM** → `applyFontsToDocument` is a no-op; **storage failure** → `removeOrphanSystemFontKey` swallows (privacy modes — cleanup is best-effort).

## Invariants

- The chosen families live only in `fontStore`; their sole projection onto the document is the inline `--font-sans`/`--font-mono` pair on `<html>`, written by `applyFontsToDocument` (idempotent, no-op without a document, applied before every `set`).
- The persisted payload contains exactly `{uiFontFamily, monoFontFamily, uiFontSmoothing, monoFontSmoothing}` under `c0wrk-fonts` v2; detections are session state and are never persisted.
- Every `applyFontsToDocument` call writes or removes the family and smoothing properties as one four-property set — a partial apply never resets an untouched knob.
- `GetSystemFonts` and `ListFontFamilies` never fail to the renderer: unavailability is the zero/empty response plus a Debug log.
- A stored family never contains `"` or `\` and is non-empty (`normalizeFamily`); a applied value always leads with one double-quoted family followed by the stock stack, or the property is absent.
- `null` (or no choice) ⇒ no inline `--font-sans`/`--font-mono` on `<html>` ⇒ rendered fonts byte-identical to a build without the feature; `''` smoothing ⇒ no `--font-smoothing-*` on `<html>` ⇒ rendering identical to a build without the feature. The mono knob's sole projection is the monospace-surface rule list — sans surfaces are unreachable from it.
- Detection never repaints: only `setUIFontFamily`/`setMonoFontFamily` (and `Use system` through them) move the document.
- The monospace picker offers only fontconfig-mono families (`fc-list :mono`); the interface picker offers every installed family; the two enumerations are independent — one failing never empties the other.
- Every font preview (dropdown labels and the closed field's value) goes through `composeFontFamily` via `StringCombobox`'s `itemStyle`/`inputStyle`; no component carries a raw `fontFamily` literal (pinned by the type-scale guard's shape rule and the FontSettings/StringCombobox tests).
- No CodeMirror theme bakes a literal font stack — `.cm-content`/`.cm-scroller` carry the `--font-*` vars (pinned by `cmTheme.test.ts` / `cmChatTheme.test.ts` against the mounted stylesheet), and the CM hosts reconfigure on the font choice (pinned by the MiniCodeMirrorField wiring test).
- The icon font (`--font-icon`, SauceCodePro NF) and the type scale (14px base + UI Scale) are unaffected in every state.

## Extension Points

- **Another desktop environment / platform detection** (e.g. KDE, macOS `defaults`): add a build-tagged reader following `backend/systemfont_linux.go` (argv as compile-time constants, bounded timeout, error = "unavailable") behind the same `readSystemFonts` name — nothing else changes.
- **Another enumeration source** (e.g. macOS/Windows font listing): implement `listFontFamilies` per platform behind the same seam; the RPC, guards, and UI are source-agnostic.
- **New persisted fields** in `fontStore`: bump `version` and implement `migrate` (the store carries the convention comment).
- **Live desktop-font following** would need a new trigger around the existing fetch path — the read-once-per-launch contract and the restart caveat in the UI text change together.

## Related Specs

- [../../decisions/077-font-smoothing-selection.md](../../decisions/077-font-smoothing-selection.md) — the smoothing decision: the CSS lever, per-surface scoping, and the best-effort stance
- [../../decisions/076-user-font-selection.md](../../decisions/076-user-font-selection.md) — the five decisions this feature implements
- [../../domains/fonts.md](../../domains/fonts.md) — the installed-family enumeration pipeline behind `ListFontFamilies` (what is installed — different question from what the desktop configured)
- [stores.md](stores.md) — `fontStore` in the store catalog
- [ui-scale.md](ui-scale.md) — owns the type scale this feature deliberately does not touch (14px base + zoom)
- [README.md](README.md) — frontend architecture overview
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — the `GetSystemFonts` / `ListFontFamilies` RPC surface (binding regenerated on the next `wails build`/`wails dev`)
