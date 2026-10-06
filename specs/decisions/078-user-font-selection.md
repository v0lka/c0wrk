# ADR-078: User Font Selection (chosen UI/mono families replace follow-system-font)

## Status

Accepted

## Context

The first font feature was an opt-in "Follow system font" toggle: a single persisted boolean, a single detected GNOME interface family, and one inline `--default-font-family` override. It could not express what users actually ask for: choosing a *specific* family for the UI and a *different* one for monospace (the embedded terminal in particular needs its own mono choice), typing a family name on systems where nothing is detectable, or picking from the fonts actually installed on the host.

The underlying mechanics stay as they were: the WebKitGTK webview does not follow `gtk-font-name`, so any family — system-configured or user-chosen — must be carried into the webview explicitly through the app's own CSS; there is no live propagation channel, so a desktop-side font change applies on the next launch; and c0wrk owns its type scale, so only the family name ever crosses the boundary, never a size or style.

The decision replaces the boolean follow mode with a general user font-selection model and records the five sub-decisions that shape it.

## Decision

**1. Build-tagged readers with stubs for every OS probe.** Both backend probes are platform-split at compile time, following the established desktop-integration pattern (fixed compile-time argv, bounded child-process timeout, error means "not available here"):

- Desktop-environment detection (`readSystemFonts`, `backend/systemfont_linux.go` / `backend/systemfont_other.go`): Linux reads `org.gnome.desktop.interface` `font-name` + `monospace-font-name` via `gsettings` (3 s per key); `!linux` is a stub returning `errSystemFontUnavailable`.
- Installed-family enumeration (`listFontFamilies`, `backend/fontlist_linux.go` / `backend/fontlist_other.go`): Linux runs `fc-list [:mono] --format '%{family}\n'` (5 s — fc-list may rebuild its cache; the RPC's `monospace` flag selects the `:mono` narrowing — only the families fontconfig tags as monospace); `!linux` is a stub returning `errFontListUnavailable`.

Both RPCs (`GetSystemFonts`, `ListFontFamilies` in `backend/frontend_api_system.go`) turn every reader error into the zero/empty response plus a Debug log — "the probe does not exist here" is a normal outcome, never a rejected Promise. Tests inject readers through the `readSystemFontsFn` / `listFontFamiliesFn` seams on `FrontendAPI`.

**2. Editable comboboxes with a prepend fallback.** The picker is a free-text `StringCombobox` per family, not a closed list: the offer order is the `Default` sentinel (mapped to the store's `null`), the session-detected system family, the current custom value, then the installed families from `ListFontFamilies` — every family for the interface picker, only the fontconfig-mono families (the `:mono` listing) for the monospace picker, whose detection/current offers stay regardless of what the enumeration returned. Because input is free text, the feature works identically on every OS — where detection/enumeration come up empty the dropdown still offers Default + typed input. Whatever is committed is applied as `composeFontFamily(family, stack)` (`frontend/src/lib/fonts.ts`): the family as one double-quoted CSS token **prepended to the stock stack**, so a missing/renamed user font degrades to the stock stack, never to a bare serif default. Every option — and the current value in the closed field — renders in its own typeface (`StringCombobox` `itemStyle`/`inputStyle` fed by `composeFontFamily`: `Default` previews the stock stack it stands for; the mono picker's previews fall back to the mono stack).

**3. Dual detection — "what did we find", not "follow what you found".** `GetSystemFonts` replaces the single-family `GetSystemUIFont`: it detects both the interface and the monospace family and parses each description independently (`parseGnomeFontName`), so an unparsable value empties only its own family. There is no availability flag — an empty string is the wire's "not detected". Detection is candidate discovery, not a mode: `useSystemFonts` feeds the store's session detections once per launch, the settings always render, and the `Use system` button applies whatever was found (both families, or the one the desktop reported) and stays disabled until at least one family is detected.

**4. Clean reset.** `null` is the "default" sentinel in `fontStore`: the custom property is `removeProperty`'d from `<html>`, and the stock stack applies byte-identically to a build without the feature. `normalizeFamily` collapses empty/quote-only input back to `null` at the store, and the combobox's `Default` entry maps to it. The replaced boolean feature's persisted key (`c0wrk-follow-system-font`) has no successor in the chosen-family model — it is **retired, not migrated**: `removeOrphanSystemFontKey` removes it at startup.

**5. Token-override delivery.** The values ride the exact Tailwind v4 `@theme` token names `--font-sans` / `--font-mono` (`frontend/src/index.css`): `applyFontsToDocument` writes inline custom properties on `<html>`, and an inline declaration beats the `:root` theme rule for the same element in the cascade — so no `index.css` change is involved, the `font-sans`/`font-mono` utilities follow automatically, and `main.tsx` re-applies pre-paint (before React renders) for FOUC-free startup. The icon font (`--font-icon`, SauceCodePro NF) is outside the mechanism.

## Consequences

**Positive**

- One mechanism serves UI and mono independently; the terminal consumes the mono choice live (xterm `fontFamily` + re-fit) without a second store.
- Works on every platform: choice never depends on a detectable desktop.
- The `@theme`-token override keeps delivery side-effect-free for CSS — themes, index.css and the type scale are untouched.
- Missing-font degradation is graceful by construction (prepended stock stack).

**Negative**

- Two Linux-only child-process probes per relevant surface (once per launch for detection, two `fc-list` runs per settings open for enumeration — the full list and the `:mono` list); on other platforms the probes always fail and cost a Debug log.
- No live following of desktop font changes (WebKitGTK limitation) — the UI states the restart caveat.
- Free-text family names cross the wire from OS metadata: the `api/fonts` boundary must sanitize (`sanitizeFontName`, `MAX_FONT_NAME_LENGTH`) and the store must re-normalize, since the values are interpolated into CSS tokens.
- The persisted model changed shape (`c0wrk-follow-system-font` → `c0wrk-fonts`); users of the old opt-in silently return to the default typeface — accepted, because a boolean maps to no family.

## Alternatives Considered

- **Keep and extend the follow-system-font toggle** — rejected: a single boolean + one family cannot express separate UI/mono choices, custom families, or "pick from installed"; the follow behavior survives only as the `Use system` shortcut.
- **Migrate the legacy flag into the new store** — rejected: `{followSystemFont: boolean}` carries no family; the only honest mapping is retirement (startup `removeItem`), so a stale key cannot linger.
- **Closed dropdown over installed families only** — rejected: it would break on platforms without fontconfig and prevent choosing a family by name before it is installed; free text with prepended fallbacks is strictly more capable.
- **fontconfig/GObjects cgo linkage instead of exec'ing `fc-list`/`gsettings`** — rejected: exec keeps the build cgo-neutral and matches the codebase's desktop-probe pattern (`config/shell_env.go`, `session/clipboard_linux.go`); the reads are one-shot, so spawn cost is irrelevant.
- **Separate UI-font and mono stores** — rejected: one `fontStore` gives one persistence key, one pre-paint apply, and an atomic `setDetectedFonts` (a single `set` = one store notification for the dual detection result).

## Related Specs

- [domains/frontend/fonts.md](../domains/frontend/fonts.md) — the feature domain spec this decision underpins
- [domains/fonts.md](../domains/fonts.md) — the installed-family enumeration pipeline (`fc-list` parsing) behind `ListFontFamilies`
