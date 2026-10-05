# System Font (Follow Desktop UI Font)

## Role

Defines the opt-in "Follow system font" setting: the app renders its UI with the desktop environment's configured UI **font family** (the GNOME interface font via `gsettings` on Linux) instead of c0wrk's default typeface, and the exact mechanism that carries that family into the WebKitGTK webview.

## Motivation

The Linux webview (WebKitGTK) does **not** follow `gtk-font-name`: a native GTK widget would inherit the desktop's UI font automatically, but the webview's fonts are decided entirely by the app's own CSS. On a GNOME desktop the app therefore has to read the system font choice itself and apply it explicitly. This conclusion comes from the research that produced the feature (recorded in the doc comment of `GetSystemUIFont` in `backend/frontend_api_system.go`): there is no live propagation channel either — a font changed in GNOME's settings applies to c0wrk only on the next launch, because the value is read once per app run.

The feature is deliberately narrow: only where a system UI font is actually detectable (Linux + GNOME `gsettings` with the `org.gnome.desktop.interface` schema) and only the family name — see [Boundaries](#boundaries).

## Key Files

- `backend/frontend_api_system.go` — the `GetSystemUIFont` RPC (`SystemUIFontResponse{Available, FontFamily}`) and its seam dispatch; every "not detected" outcome returns the zero response, never an error
- `backend/frontend_api.go` — the `readSystemFontFn` test seam field on `FrontendAPI` (nil in production → the platform reader runs), mirroring `readProcessRSSFn`
- `backend/systemfont.go` — platform-neutral `parseGnomeFontName` (strips GVariant quoting, the trailing point size, and trailing Pango style keywords)
- `backend/systemfont_linux.go` — Linux reader: `exec.LookPath("gsettings")` + `gsettings get org.gnome.desktop.interface font-name` under a 3 s timeout (`gnomeFontTimeout`)
- `backend/systemfont_other.go` — `!linux` stub: always "unavailable"
- `backend/systemfont_test.go` — parser table, RPC tests through the seam, live-gsettings test (skips when absent)
- `frontend/src/api/systemFont.ts` — `getSystemUIFont()` RPC wrapper with the boundary type guard (`isSystemUIFontResponse`); `available=false` → `null`
- `frontend/src/hooks/useSystemFont.ts` — App-root loader: fetch on mount, retry on `backend:ready`, latch on any definitive answer
- `frontend/src/stores/systemFontStore.ts` — the store (`followSystemFont` flag + session `systemFontFamily`), `applySystemFontToDocument`, `SYSTEM_FONT_CSS_VAR`, persistence under `c0wrk-follow-system-font`
- `frontend/src/App.tsx` — mounts `useSystemFont()` once at the app root (works in every app phase)
- `frontend/src/components/settings/SystemFontToggle.tsx` — the Appearance-tab switch block
- `frontend/src/components/settings/SettingsModal.tsx` — mounts `SystemFontToggle` on the Appearance tab below `UIScaleSelector`
- `frontend/src/index.css` — imports `tailwindcss` (preflight is what consumes the variable — see [CSS mechanics](#css-mechanics-tailwind-v4-preflight-variable)); its `@theme` block overrides colors only, never fonts

## Behavior

### RPC contract

`GetSystemUIFont() SystemUIFontResponse` on `*backend.FrontendAPI` (wire shape: `{available: boolean, font_family: string}`):

- `Available=true` + a non-empty `FontFamily` — a desktop UI font was detected and parsed.
- `Available=false` + `FontFamily=""` (zero response) — every "not detected" outcome: non-Linux build (stub reader), no `gsettings` in `PATH`, schema absent (KDE, bare WM), reader error, or an unparsable value. Unavailability is a normal answer, not an RPC failure.
- The read goes through the `readSystemFontFn` seam; a reader error is logged at Debug only (nil-guarded logger) — a desktop without gsettings must not generate noise.
- The Linux reader execs `gsettings get org.gnome.desktop.interface font-name` (argv is a compile-time constant; nothing user- or model-controlled reaches it) under `gnomeFontTimeout` = 3 s, so a wedged settings daemon can never hang the RPC. The value is read once per app launch.
- `parseGnomeFontName` reduces the raw Pango description (`'Noto Sans 11'`, `'DejaVu Sans Bold 10'`, `'Cantarell 10.5'`) to the family: strips single **or** double GVariant quoting, then trailing size tokens (plain decimal with at most one dot — `strconv.ParseFloat` is deliberately avoided so `1e3`/`+11` are never over-stripped) and trailing Pango style keywords (`Bold`/`Italic`/`Oblique`/`Light`/`Regular`/`Medium`/`Heavy`/`Thin`, stripped only while a family token remains below them). An empty survivor (`''`, `'11'`) → not ok → zero response.

### Delivery to the UI

`frontend/src/api/systemFont.ts` wraps the RPC (components never import `wailsjs` directly): `isSystemUIFontResponse` validates the boundary (a boolean `available` + string `font_family`; a non-empty family is required only when `available=true` — an empty family with `available=true` would be schema drift, since the parser never returns an empty ok); `available=false` maps to `null` with no logging (a normal outcome); malformed data raises `TypeError`; backend/runtime errors are logged via `logger.error` and re-thrown.

`useSystemFont` (mounted once in `frontend/src/App.tsx`) fetches on mount and, because the mount request can arrive before the backend is ready (splash phase), retries on the global `backend:ready` event — without latching on failure, replaying a retry that arrives while a fetch is in flight, and latching on **any** successful answer (`available=false` is definitive; retrying it cannot change the outcome). A late answer after unmount is ignored.

### Store and persistence

`systemFontStore` (mirror of `uiScaleStore`):

- `followSystemFont: boolean` — **default OFF (opt-in)**; the ONLY persisted field, under the localStorage key `c0wrk-follow-system-font` (`partialize`, `version: 1`, identity `migrate`). Existing users keep c0wrk's own typeface until they flip the switch.
- `systemFontFamily: string | null` — the family detected this session; session-scoped by design (a persisted family would go stale across a system-side font change). Cached even while the flag is off, so a later toggle-on needs no re-fetch.
- Both actions call `applySystemFontToDocument` **before** `set`, so the effect on `<html>` is immediate — there is no apply/save step anywhere in the UI.

### CSS mechanics (Tailwind v4 preflight variable)

Tailwind v4 (imported in `frontend/src/index.css`) consumes a theme variable named exactly `--default-font-family`: preflight sets the base font as `html, :host { font-family: var(--default-font-family, <default sans stack>) }`, and the theme layer defines `--default-font-family` on `:root` (the default sans stack — the project's `@theme` overrides colors only). The feature rides on that:

- `applySystemFontToDocument(follow, family)` writes the inline declaration `--default-font-family: "Family"` on `document.documentElement` (`SYSTEM_FONT_CSS_VAR` exports the name). An inline style on `<html>` beats the `:root` theme rule for the same element in the cascade, so preflight's base `font-family` resolves to the system family — **no `frontend/src/index.css` rule is added or needed**.
- The value is always a double-quoted family (`"Noto Sans"`), so multi-word names stay a single CSS token; the names come from the backend parser, which strips GNOME's own quoting and cannot contain a double quote.
- When following is off or no family is known, the property is `removeProperty`'d and the Tailwind default sans stack returns untouched.
- Only the **sans** base chain moves. The mono side (`--default-mono-font-family`, `font-mono`, the embedded `SauceCodePro NF` icon/mono stacks) never sees the variable.
- The function is a no-op without `document` (tests) and idempotent — safe to call repeatedly.

### Settings UI

`SystemFontToggle` renders on the Appearance tab below `UIScaleSelector`: a "System Font" header, the shared `Toggle` from `ModelProfilesControls`, and a muted caption — `detected: <family>` when a family is known. The toggle description states that font changes on the desktop apply after restarting c0wrk.

Visibility is two-sided: the block renders only when a family was detected this session (`systemFontFamily !== null`) **or** the user's persisted opt-in is on. On KDE/Windows/macOS with the default-off flag nothing renders at all ("the setting does not exist here"); a flag left on from a GNOME session keeps the block visible with a muted `not detected` note instead of silently vanishing, so the opt-in can still be seen and undone.

## Boundaries

The feature is intentionally bounded; each boundary is a design decision, not an accident:

- **Family only.** The point size and Pango style tokens are stripped in the parser and never cross the RPC. c0wrk owns its type scale (14px base + the UI Scale setting, see [ui-scale.md](ui-scale.md)) and applies weight/slant through CSS; GNOME's point size would fight both.
- **Mono is untouched.** `font-mono`, `--default-mono-font-family`, and the SauceCodePro NF stacks are outside the feature.
- **Linux + GNOME/gsettings only.** Other platforms and desktops report "unavailable" — the setting is hidden, not error-styled. Nothing degrades.
- **Restart to pick up desktop changes.** The font is read once per app launch; WebKitGTK offers no live propagation channel. The UI says so.
- **Default OFF.** Opt-in via the persisted `c0wrk-follow-system-font` flag; the detected family is never persisted.

## Error Handling

- **Reader unavailable/error (backend)** → zero response (`Available=false`) + Debug log with nil-guard; the frontend hides the setting. Never an RPC error, never user-visible noise.
- **Unparsable gsettings value** → zero response (same path).
- **Malformed wire data (frontend)** → `TypeError` from the type guard; nothing half-shaped enters stores.
- **RPC/runtime failure** → logged in `frontend/src/api/systemFont.ts`, re-thrown; `useSystemFont` keeps the `backend:ready` retry path armed (no latch on failure) and leaves the document on whatever it already had.
- **No DOM** → `applySystemFontToDocument` is a no-op.

## Invariants

- The followed family lives only in `systemFontStore`; its sole projection onto the document is the inline `--default-font-family` custom property on `<html>`, written by `applySystemFontToDocument` (idempotent, no-op without a document).
- The persisted localStorage payload contains exactly `{followSystemFont}` under `c0wrk-follow-system-font`; the detected family is session state and is never persisted.
- `GetSystemUIFont` never fails: every undetectable-desktop outcome is the zero response `{available: false, font_family: ""}`.
- Only the sans family ever crosses the boundary — no size, no weight, no style tokens.
- The mono stacks (`font-mono`, SauceCodePro NF) are unaffected by the feature in every state.
- Off (or no family) ⇒ no `--default-font-family` declaration on `<html>` ⇒ the rendered base font is byte-identical to a build without the feature.
- The Appearance block never renders for a user who has neither a detected family nor a persisted opt-in.

## Extension Points

- **Another desktop environment** (e.g. KDE Plasma): add a build-tagged reader following `backend/systemfont_linux.go` (argv as compile-time constants, bounded timeout, error = "unavailable") behind the same `readSystemFontName` name; nothing else changes.
- **New persisted fields** in `systemFontStore`: bump `version` and implement `migrate` (the store carries the convention comment).
- **Re-reading at runtime** (live follow of desktop font changes) would require a new trigger around the existing fetch path; the current read-once-per-launch contract and the restart caveat in the UI text would both need updating together.

## Related Specs

- [stores.md](stores.md) — `systemFontStore` in the store catalog
- [ui-scale.md](ui-scale.md) — owns the type scale this feature deliberately does not touch (14px base + zoom)
- [README.md](README.md) — frontend architecture overview
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — Wails RPC surface (binding regenerated on the next `wails build`/`wails dev`)
