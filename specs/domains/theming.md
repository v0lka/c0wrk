# Theming

## Purpose

c0wrk ships two built-in themes (Default Dark / One Dark, Default Light / One Light) and lets users import custom CSS themes; a set of ready-to-import bundled palette themes ships in the repo at `frontend/src/assets/themes/` (see "Bundled Themes"). A theme is a set of CSS custom-property (design-token) overrides on `:root`; all component styles reference `var(--color-*)` / `var(--radius-*)` exclusively, so a theme recolors the entire UI — including the embedded terminal (XTerm ANSI mapping) and syntax highlighting — without touching any component code.

## Key Files

- `backend/theme_sanitize.go` - theme CSS sanitizer: a real CSS tokenizer (`github.com/gorilla/css/scanner`) parses the document and keeps ONLY custom-property declarations on a single `:root` rule (plus the metadata header and one `color-scheme` declaration); every other construct — at-rules, other selectors, ordinary declarations, non-allowlisted functions such as `image-set()`, non-`data:` URLs, unbalanced parens, and any kept token carrying a CSS escape (the webview decodes it after injection, so escapes fail closed) — is rejected outright. The sanitizer emits a canonical `:root{…}` document that cannot reference any resource
- `frontend/src/index.css` - design-token single source of truth: `@theme` block (One Dark defaults) + `[data-theme="light"]` override block (One Light); all component styles cascade from these variables
- `frontend/src/stores/themeStore.ts` - Zustand store (persisted): active `themeId`, CSS cache for the active custom theme, builtin-theme table, `applyThemeToDocument`, `selectActiveThemeType` selector
- `frontend/index.html` + `frontend/public/prepaint-theme.js` - pre-paint anti-FOUC apply: a blocking EXTERNAL script (inline scripts are disallowed by the production CSP `script-src 'self'`) that reads the persisted v3 store from `localStorage['c0wrk-theme']` and mirrors `applyThemeToDocument` — `data-theme` from the type, `data-custom-theme` + scoped style injection for a custom id
- `frontend/src/main.tsx` - pre-paint apply: reads the persisted store synchronously (zustand-persist rehydrates from localStorage synchronously) and applies `data-theme` + injects the cached custom-theme CSS before React renders anything
- `frontend/src/api/themes.ts` - typed RPC wrappers (`listThemes`, `pickAndImportThemes`, `deleteTheme`) over the generated Wails bindings
- `frontend/src/components/settings/ThemeSelector.tsx` - settings UI: the theme dropdown (built-ins then installed customs), the import button, and the one-click hover-delete overlay on custom rows
- `backend/themes.go` - pure theme logic: `ThemeDTO`, `ParseThemeCSS`, `ValidateThemeCSS`, `themeSlug`
- `backend/frontend_api_themes.go` - Wails-exposed methods: `ListThemes`, `DeleteTheme`. The import functions (`importThemeFromPath` / `importThemesFromPaths`) are deliberately UNEXPORTED package-level functions — exported methods on FrontendAPI are auto-bound to the renderer, and a path-taking import RPC must not be callable from compromised renderer JS; `desktop.App.PickAndImportThemes` (native picker) is the sole import entry point
- `desktop/app.go` - `PickAndImportThemes`: native multi-select file dialog + batch import in one action (requires Wails context)
- `backend/config/paths.go` - `ThemesDir(agentDir)` → `<agentDir>/themes` (theme storage root)
- `frontend/src/hooks/useXTermTheme.ts` - resolves XTerm ANSI colors from CSS variables at call time (re-resolves on theme change)
- `frontend/src/lib/cmChatTheme.ts`, `frontend/src/components/fileViewer/CodeMirrorFileViewer.tsx` - CodeMirror themes resolved from CSS variables; re-created via Compartment on theme change
- `frontend/src/assets/themes/example-theme.css` - fully annotated example theme (light, "Solar Light"): a self-contained authoring tutorial — metadata header, validation rules, every token group explained inline; guarded by `TestValidateThemeCSS_SpecExampleTheme`
- `frontend/src/assets/themes/` - bundled palette themes (plus the example theme), ready to import as-is (see "Bundled Themes" below); each guarded by `TestValidateThemeCSS_BundledThemes` in `backend/themes_test.go`
- `desktop/csp.go` / `desktop/csp_dev.go` - production CSP middleware (`!dev` build) stamping a strict `Content-Security-Policy` on every asset response — the webview-level backstop that blocks any fetch an injected stylesheet might attempt, even if a future sanitizer bypass slipped through. No-op under `wails dev` (the `dev` build tag) so Vite/HMR keeps working

## Core Types

```go
// backend/themes.go
type ThemeDTO struct {
    ID   string `json:"id"`
    Name string `json:"name"`
    Type string `json:"type"` // "dark" | "light"
    CSS  string `json:"css"`  // the canonical SANITIZED theme body; present on
                              // every entry (import result and list entry) so
                              // the frontend can activate any theme without a
                              // second RPC
}

// backend/frontend_api_themes.go
type ThemeImportResult struct {
    File  string    `json:"file"`           // source path as picked
    Theme *ThemeDTO `json:"theme,omitempty"` // set on success
    Error string    `json:"error,omitempty"` // failure reason; set on failure
}
// Exactly one of Theme/Error is set; batch imports validate each file
// independently — one invalid file never blocks the rest of the batch.
```

```ts
// frontend/src/stores/themeStore.ts
interface ThemeState {
    themeId: string           // persisted; builtin id or custom slug, default 'default-dark'
    themeCss: string          // persisted; cached SANITIZED CSS of the active custom theme ('' for builtins)
    themeType: ThemeType      // persisted (v3); dark|light kind of the active theme
    customThemes: ThemeInfo[] // NOT persisted; refreshed via loadThemes()
}

interface ThemeActions {
    /** Activate a theme and cache its CSS + type (custom themes only). */
    setTheme: (id: string, css?: string, type?: ThemeType) => void
    /** Replace the custom-theme catalog; reset to Default Dark when the
     *  active custom theme disappears from the list. */
    applyThemes: (list: ThemeInfo[]) => void
    /** Fetch the backend catalog into applyThemes. Safe to call repeatedly;
     *  errors are swallowed (builtins keep working when the backend is not
     *  ready). */
    loadThemes: () => Promise<void>
}

export const BUILTIN_THEMES = [
    { id: 'default-dark', name: 'Default Dark', type: 'dark' },
    { id: 'default-light', name: 'Default Light', type: 'light' },
] as const
```

## Flow

```
Import:  [settings: import button] → desktop.PickAndImportThemes()
              → wailsRuntime.OpenMultipleFilesDialog (filter *.css, multi-select)
              → backend.ImportThemesFromPaths(f, paths) (package-level bridge;
                 the import funcs are unexported so the renderer can never call
                 them with an arbitrary path)
                   → per file: stat + size cap → read (regular files only) →
                     sanitizeThemeCSS → themeSlug → install the CANONICAL
                     sanitized CSS into ThemesDir as <slug>.css (atomic write)
                     → ParseThemeCSS → ThemeDTO{id, name, type, css}
                   → one ThemeImportResult per input path, input order preserved
         ← ThemeImportResult[] → loadThemes() catalog refresh →
           themeStore.setTheme(last successful id, css, type)
           → the last successful import activates immediately — the CSS body
             rides with every result and list entry, so activation never needs
             a second RPC; failed files (invalid CSS, unreadable, reserved id)
             are reported via a runtime_error toast while the valid ones still install

Apply:   themeStore.setTheme(id, css, type)
              → builtin: data-theme = 'dark' | 'light'; remove data-custom-theme and
                         <style id="c0wrk-custom-theme">
              → custom:  data-theme = <type>  (dark|light — native controls and the
                           One Light override block key off the TYPE, never a slug)
                         data-custom-theme = <id>
                         single <style id="c0wrk-custom-theme"> at the END of <head>
                         whose textContent = css with every :root selector re-scoped
                         to :root:root[data-custom-theme="<id>"] (scopeThemeCSS)

Cascade contract: the One Light override in index.css (:root[data-theme="light"])
is UNLAYERED with specificity (0,2,0), and the pre-paint script injects the
custom <style> BEFORE the app stylesheet <link> is parsed — document order
between the two is not deterministic. The scoped selector therefore doubles
the :root pseudo-class (:root:root[...] = (0,3,0)) so a light-type custom
theme outranks the One Light override by specificity, never by order; and
applyThemeToDocument re-homes the reused <style> element to the end of <head>
(the order tie-breaker as a second line of defense). Both copies of the
scoping logic — scopeThemeCSS (themeStore.ts) and the inline join in
public/prepaint-theme.js — MUST stay in sync: the selector shape is a
correctness invariant, not a formatting choice.

Startup: index.html loads public/prepaint-theme.js — a blocking EXTERNAL script
              (CSP: script-src 'self'; inline scripts are not allowed) that reads
              localStorage['c0wrk-theme'] (v3 {themeId, themeCss, themeType} or
              v2 with the type derived) and mirrors applyThemeToDocument:
              data-theme=<type>, and for a custom id data-custom-theme=<id> plus
              the scoped style injection — correct palette before first paint
         main.tsx (still before React renders) — the authoritative apply:
              → getState() rehydrates synchronously from localStorage
              → applyThemeToDocument(themeId, themeCss, type)
         App.tsx (on mount, and again on backend:ready) → loadThemes() →
              listThemes() → applyThemes(list) reconciliation

Delete:  [settings: trash icon on a custom item] → api.deleteTheme(id)
              → backend.DeleteTheme(id) resolves the id to the installed entry
                whose slug matches, then removes that <ThemesDir>/*.css file
              → loadThemes() refresh → applyThemes(list)
              → if the deleted theme was active → reset to default-dark + clear CSS cache
```

## Theme File Format

A theme file is a plain UTF-8 CSS file with a `.css` extension. Only CSS custom-property declarations on `:root` (and the metadata header below) are used; custom themes are injected as a single `<style>` element, they are NOT compiled through Tailwind — an `@theme` block in a custom theme is ordinary CSS (the `--color-*` variables still work as plain CSS variables) but the Tailwind-specific `@theme` semantics (utility generation) do not apply.

### Metadata header (required for explicit name/type)

The header comment must open the file (leading whitespace allowed; a `c0wrk-theme` marker anywhere else in the body is ignored):

```css
/* c0wrk-theme: <name> | <dark|light> */
```

- Type matching is case-insensitive (`Dark`, `LIGHT` accepted); the name is free text.
- Without the header, metadata falls back per the chain below.

### Metadata fallback chain

| Priority | Name | Type |
| -------- | ---- | ---- |
| 1 | `/* c0wrk-theme: Name | light */` header (must be at file start) | header type token |
| 2 | prettified filename (`nord.css` → `Nord`; `-`/`_` → spaces, each word capitalized; empty → `Theme`) | `color-scheme: (dark\|light)` declaration in the CSS body (`prefers-color-scheme` media features do NOT count) |
| 3 | — | `dark` (final fallback) |

### Required tokens

A theme MUST declare the two tokens the validator treats as a minimum viable theme:

- `--color-background`
- `--color-foreground`

All other tokens are optional; undeclared tokens keep the One Dark default from `@theme`, so a minimal theme inherits the rest of the palette. (The bundled example theme declares the full set — see the authoring reference below.)

### Validation rules (import gate)

`ValidateThemeCSS` — the gate the example-theme and bundled-theme tests call —
enforces the 512 KiB size cap (`maxThemeCSSSize`), then hands the document to
`sanitizeThemeCSS` (see `backend/theme_sanitize.go`), which PARSES the CSS
with a real tokenizer and enforces an allowlist — nothing is rejected by
pattern-matching strings, so no syntax (`image-set()`, `-webkit-image-set()`,
escaped identifiers like `\75 rl(`, HTML tokens) can smuggle a resource
reference past it. The import path applies the same two rules with the cap
checked twice around the read — a `stat` before opening (the full-file
allocation is itself the DoS) and a length check after it — and
`safeio.ReadFile` refuses a non-regular file instead of blocking the RPC.
A theme is accepted only when its entire shape is:

| # | Allowed shape | Notes |
| - | ------------- | ----- |
| 1 | Optional `/* c0wrk-theme: <name> \| <dark\|light> */` header comment, at file start | preserved verbatim in the sanitized output |
| 2 | Exactly one top-level `:root { … }` rule | no other selectors, no combinators, no at-rules (`@import`, `@media`, `@theme`… all rejected) |
| 3 | Inside the rule: only custom-property declarations (`--token: value;`) plus at most one `color-scheme: dark\|light;` | ordinary declarations (`color:`, `background:`…) are rejected |
| 4 | Values may use: identifiers, `#hex`, numbers, dimensions, percentages, quoted strings, `url(data:…)` tokens, the `,` and `/` separators, `!important`, and the functions `rgb( rgba( hsl( hsla( hwb( var( calc( min( max( clamp(` (nested, balanced); inside a function's arguments the `-` `*` `+` operators are allowed as well | any other function, any non-`data:` URL, any other punctuation, and any token carrying a CSS escape is rejected |
| 5 | `--color-background` and `--color-foreground` must be declared | minimum viable theme |

The sanitizer emits (and the import stores) the CANONICAL form: the header
comment plus a single normalized `:root { … }` block. Declarations keep their
source order, a repeated custom property collapses to its last value (the CSS
cascade), and the single `color-scheme` declaration closes the block. What
lands in `~/.c0wrk/themes/<slug>.css`, what `ListThemes` returns, and what the
webview injects is always the sanitized output — never the raw source.
`ListThemes` re-sanitizes every file on read as defense in depth (a hand-edited
installed file that fails sanitization is skipped, not shipped to the webview);
it also skips — each with a Warn, so one bad entry never breaks the listing — a
non-regular `*.css` entry, one over the size cap, one it cannot read, and one
whose name slugifies to nothing or to a reserved id.

A second, webview-level backstop is the strict production CSP
(`desktop/csp.go`, `!dev` builds): `default-src 'none'; script-src 'self';
style-src 'self' 'unsafe-inline'; img-src 'self' data: https: http:; connect-src 'self';
…` — even a hypothetical future sanitizer bypass cannot turn an injected
stylesheet into a network request.

### Slug / identity

- The theme ID is derived from the **source filename**: base name without extension, lowercased, restricted to `[a-z0-9-]` with runs of disallowed characters collapsed to single hyphens and edge hyphens trimmed; the result must be non-empty. `My Cool Theme.css` imports as `my-cool-theme.css`, ID `my-cool-theme`.
- The reserved IDs `default-dark`, `default-light`, `dark`, and `light` cannot be used by imported themes. `dark`/`light` are the `data-theme` attribute keys; since custom CSS is injected scoped to `:root:root[data-custom-theme="<id>"]` and `data-theme` only ever carries the TYPE, the aliasing channel is closed twice (reserved slugs AND the separate attribute namespace).

### Storage

Imported themes live as `~/.c0wrk/themes/<slug>.css` (`config.DefaultAgentDir = ".c0wrk"`; path via `ThemesDir(agentDir)` in `backend/config/paths.go`). The directory is created on demand as a REAL directory (`safeio.MkdirAllReal`, so a planted symlink fails the import closed), and the canonical body lands through an atomic temp-file rename (`safeio.WriteFileAtomic`) that leaves the previous copy intact when a write fails; listing a missing directory returns an empty list, not an error.

### Re-import semantics (update)

Importing a file whose slug already exists **overwrites** the stored file — re-import is the update path. The theme ID stays stable, so the active selection (`themeId`) and UI ordering survive updates. The new CSS takes effect for the user only after the theme is (re)selected — or immediately, because the settings import flow activates the last successful import right away.

### Deleting the active theme

`DeleteTheme(id)` slugifies the id (a reserved or unusable slug is rejected), scans `<ThemesDir>` for the `*.css` entry whose slug matches, and removes that file — so a hand-dropped file with a non-canonical name is deletable too. Zero matches fail as `does not exist`; two entries slugifying to the same id fail as ambiguous instead of guessing which to remove. The themes directory must be a real directory — a symlinked one is refused, a missing one means the theme is absent. If the removed theme is currently active, `applyThemes` reconciles the store: `themeId` resets to `default-dark`, the CSS cache is cleared, and the custom `<style>` element is removed. The UI never keeps pointing at a missing file; there is no confirmation dialog (same one-click pattern as session/project deletion).

## Bundled Themes

Besides the two built-in themes compiled into `index.css`, the repo ships ready-to-import palette themes under `frontend/src/assets/themes/` (next to the annotated `example-theme.css` — the directory doubles as the author-facing sample set; it lives in the frontend tree, NOT under `specs/`, because specs describe the system while these files are shipped assets). They are ordinary custom-theme files: users import them through the same `Settings → Appearance → Theme → [+]` flow, they land in `~/.c0wrk/themes/<slug>.css`, and they follow every rule of the custom-theme format (header, tokens, validation limits). They exist so users can try known palettes without authoring CSS.

| File | Theme name | Type | Palette |
| ---- | ---------- | ---- | ------- |
| `frontend/src/assets/themes/nord.css` | Nord | dark | Nord |
| `frontend/src/assets/themes/tokyo-night.css` | Tokyo Night | dark | Tokyo Night |
| `frontend/src/assets/themes/catppuccin-mocha.css` | Catppuccin Mocha | dark | Catppuccin (Mocha) |
| `frontend/src/assets/themes/high-contrast-dark.css` | High Contrast Dark | dark | original — black canvas / white text, every pair ≥ WCAG AA (core pairs AAA) |
| `frontend/src/assets/themes/high-contrast-light.css` | High Contrast Light | light | original — white canvas / near-black text, every pair ≥ WCAG AA (core pairs AAA) |
| `frontend/src/assets/themes/catppuccin-latte.css` | Catppuccin Latte | light | Catppuccin (Latte) |
| `frontend/src/assets/themes/rose-pine-dawn.css` | Rosé Pine Dawn | light | Rosé Pine (Dawn) |
| `frontend/src/assets/themes/gruvbox-light.css` | Gruvbox Light | light | Gruvbox (light) |

Notes:

- Each bundled theme declares the full token set (core, surfaces, primary/accent, secondary/muted, semantic, borders, RGB triplets, hljs, terminal, elevation, `color-scheme`) so it is complete without inheriting One Dark defaults.
- `TestValidateThemeCSS_BundledThemes` (`backend/themes_test.go`) guards the whole directory: every file must pass `ValidateThemeCSS`, parse to its declared name/type, and round-trip its filename to its slug. A theme added to the directory must be registered in that test's `bundledThemeFiles` table.
- The two High Contrast themes are original c0wrk palettes (not derived from an external palette); their text/background pairs were measured against WCAG at authoring time.

## Anti-FOUC cache

The persisted store key is `localStorage['c0wrk-theme']` (persist version 3). The custom-theme CSS is cached in the store (`themeCss`) together with its dark/light kind (`themeType`, new in v3) so the theme applies **before the first paint**, synchronously, without waiting for the Wails bridge or any RPC.

Two pre-React passes exist and both are idempotent:

1. `frontend/public/prepaint-theme.js` — a blocking EXTERNAL script referenced from `index.html`. It replaced the old inline script (which only understood the legacy v1 `{theme}` payload and had become a no-op; inline scripts are now impossible anyway because the production CSP allows `script-src 'self'` only). It reads the persisted store (v3 `themeId`/`themeCss`/`themeType`; the v2 shape is handled by deriving the type: builtin id → its type, else a `color-scheme:` literal in the cached CSS, else `dark`) and mirrors `applyThemeToDocument`: `data-theme=<type>` for built-ins; `data-theme=<type>` + `data-custom-theme=<id>` + a `<style id="c0wrk-custom-theme">` carrying the CSS re-scoped to `:root:root[data-custom-theme="<id>"]` for a custom id (same doubled-`:root` shape as `scopeThemeCSS` — keep in sync; see the Cascade contract above). Vite copies `public/` to the dist root, so the same file is served in dev and production.
2. `main.tsx` — `applyThemeToDocument(themeId, themeCss, selectActiveThemeType(state))` from the synchronously rehydrated store, still before React renders.

Store migrations: v1 `{theme: 'light'|'dark'}` → v3 (`default-light`/`default-dark`); v2 `{themeId, themeCss}` → v3 with `themeType` derived via `deriveThemeType`.

The dark/light kind resolves in order (`selectActiveThemeType`): (1) a built-in id → its `BUILTIN_THEMES` type, (2) the persisted `themeType` (v3), (3) the live store's `customThemes` descriptor (authoritative once `loadThemes()` resolved — `applyThemes` re-applies the active custom theme with it), (4) `dark`.

## Token Reference (authoring)

Single source of truth: the `@theme` block in `frontend/src/index.css`. A custom theme declares overrides on `:root`; undeclared tokens keep their One Dark default. Complete token list (One Dark defaults):

| Group | Token | One Dark default |
| ----- | ----- | ---------------- |
| Core (REQUIRED) | `--color-background` | `#282c34` |
| Core (REQUIRED) | `--color-foreground` | `#abb2bf` |
| Surfaces | `--color-card` / `--color-card-foreground` | `#252931` / `#abb2bf` |
| Surfaces | `--color-popover` / `--color-popover-foreground` | `#252931` / `#abb2bf` |
| Primary / accent | `--color-primary` / `--color-primary-foreground` | `#abb2bf` / `#282c34` |
| Primary / accent | `--color-accent` / `--color-accent-foreground` | `#abb2bf` / `#282c34` |
| Secondary / muted | `--color-secondary` / `--color-secondary-foreground` | `#1d2025` / `#cccccc` |
| Secondary / muted | `--color-muted` / `--color-muted-foreground` | `#1d2025` / `#cccccc` |
| Semantic | `--color-destructive` / `--color-destructive-foreground` | `#e06c75` / `#282c34` |
| Semantic | `--color-success`, `--color-warning`, `--color-info`, `--color-highlight` | `#98c379`, `#d19a66`, `#61afef`, `#e5c07b` |
| Borders & inputs | `--color-border`, `--color-input`, `--color-ring` | `#1d2025`, `#1d2025`, `#abb2bf` |
| RGB triplets | `--color-primary-rgb`, `--color-success-rgb`, `--color-destructive-rgb` | `82, 139, 255`, `152, 195, 121`, `224, 108, 117` |
| highlight.js | `--color-hljs-comment`, `--color-hljs-keyword`, `--color-hljs-literal` | `#5c6370`, `#c678dd`, `#56b6c2` |
| Terminal (XTerm) | `--color-terminal-cursor`, `--color-terminal-selection`, `--color-terminal-bright-white` | `#528bff`, `#3e4451`, `#ffffff` |
| Elevation | `--color-shadow` | `rgba(0, 0, 0, 0.3)` |
| Radii | `--radius-sm`, `--radius-md`, `--radius-lg`, `--radius-xl` | `0.25rem`, `0.375rem`, `0.5rem`, `0.75rem` |
| Type scale | `--text-xs`, `--text-sm`, `--text-base`, `--text-lg` (each with a `--line-height` companion) | `0.75rem`, `0.875rem`, `1rem`, `1.125rem` |
| Font stacks | `--font-sans`, `--font-mono`, `--font-icon` | stock stacks; `--font-mono`/`--font-icon` lead with the bundled `"SauceCodePro NF"` |

The `:root[data-theme="light"]` block re-declares the color tokens (plus `color-scheme: light`) with the One Light palette; `html { font-size: 14px }` is the single absolute font size the rem scale resolves against. The `--font-*` stacks are the defaults the user font selection overrides inline on `<html>` (see [frontend/fonts.md](frontend/fonts.md)).

## Invariants

- Every CSS body that reaches the webview is the sanitizer's canonical output: the import writes it, `ListThemes` re-sanitizes on read, and the store caches that same body.
- `data-theme` always carries the dark/light TYPE; the active custom theme's identity rides `data-custom-theme`, and the injected `<style id="c0wrk-custom-theme">` is scoped to `:root:root[data-custom-theme="<id>"]`, which outranks the unlayered `:root[data-theme="light"]` override by specificity at any document order.
- The renderer's import surface is exactly `desktop.App.PickAndImportThemes` (native picker); the path-taking import functions stay unexported package-level, reachable only through the `backend.ImportThemesFromPaths` bridge.
- `themeSlug` rejects the reserved ids `default-dark`, `default-light`, `dark` and `light`, so an imported theme can never alias a built-in palette or a `data-theme` key.
- `ListThemes` always returns a non-nil slice sorted by display name; a missing themes directory is an empty list, and one unusable entry is skipped with a Warn.
- A `ThemeImportResult` carries exactly one of `Theme`/`Error`, and a batch import validates each file independently.
- Theme activation needs no second RPC: every import result and every list entry carries the canonical CSS body.
- Both pre-paint passes (`public/prepaint-theme.js`, `main.tsx`) apply the persisted theme before React renders, are idempotent, and keep the same doubled-`:root` scoping shape as `scopeThemeCSS`.
- Losing the active custom theme (deletion, or a catalog without it) resets the store to `default-dark` and clears the cached CSS.
- Component styles reference design tokens: raw hex outside `frontend/src/index.css` appears only in bundled theme assets, tests and comments, and `frontend/src` carries zero `!important` declarations. Typography token discipline is guarded project-wide by `frontend/src/test/typeScaleInvariant.test.ts` (one absolute font size — the `html` root; font stacks only via `var(--font-*)` / the `FONT_*_STACK` mirror); the color-token and zero-`!important` rules are asserted per component (e.g. `ChatBlockOverflowToolbar.test.tsx`) rather than by a project-wide scan.

## Configuration

None — theming has no `config.yaml` surface. The active theme is frontend state persisted in `localStorage['c0wrk-theme']` (persist version 3: `themeId`, `themeCss`, `themeType`). Installed themes live under `ThemesDir(agentDir)` = `<agentDir>/themes` (`config.DefaultAgentDir = ".c0wrk"`). The import limits are compile-time constants, not tunables: `maxThemeCSSSize` (512 KiB) and `reservedThemeIDs`.

## Extension Points

- **A new bundled palette**: drop the `.css` under `frontend/src/assets/themes/` and register it in the `bundledThemeFiles` table of `backend/themes_test.go`; it must pass `ValidateThemeCSS`, parse to its declared name/type, and round-trip its filename to its slug.
- **A new design token**: declare it in `@theme` (One Dark default) and in the `:root[data-theme="light"]` override, then consume it as `var(--color-*)` — component styles take the new theme support for free.
- **A new built-in theme**: extend `BUILTIN_THEMES` (`themeStore.ts`), the `BUILTIN` type map in `public/prepaint-theme.js`, `reservedThemeIDs` (`backend/themes.go`) and add the palette block to `index.css`.
- **A wider value allowlist**: add the function name to `themeFuncAllowlist` in `backend/theme_sanitize.go` — a security-reviewed change, since every new function is a potential resource-reference channel.

## Related Specs

- [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the `ListThemes` / `DeleteTheme` / `PickAndImportThemes` RPC surface and the "no renderer-facing import RPC" rule
- [architecture/security-model.md](../architecture/security-model.md) — the `~/.c0wrk` fixed-path state-surface inventory that lists the installed theme files (`backend/frontend_api_themes.go`)
- [web-access.md](web-access.md) — the browser transport serves the same production CSP (`desktop/csp.go`)
- [terminal.md](terminal.md) — the embedded terminal that consumes the ANSI palette `useXTermTheme` resolves from these tokens
- [fonts.md](fonts.md), [frontend/fonts.md](frontend/fonts.md) — the `--font-*` tokens and the user font override that writes them inline on `<html>`
- [frontend/stores.md](frontend/stores.md) — `themeStore` in the Zustand store catalog
