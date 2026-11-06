# UI Scale (Frontend Zoom-Safety)

## Role

Defines the app-wide UI scale feature (the "UI Scale" setting) and the **zoom-safety invariant** every frontend change must uphold: the scale is applied as CSS `zoom` on `<html>`, which changes the meaning of `length` values and the coordinate space of pointer geometry, so sizing and popover placement must be written in a zoom-aware way.

## Key Files

- `frontend/src/stores/uiScaleStore.ts` — scale state (`50`–`200`%, default `100`, persisted under `c0wrk-ui-scale`), `setScale` (normalize → clamp → apply → notify), `applyScaleToDocument` (writes `style.zoom` and the `--ui-zoom` custom property on `<html>`), `getUiZoomFactor` (live factor for non-React code), `dispatchUiResizeNotification` (window `resize` so open floating-ui popovers recompute), `UI_SCALE_MIN`/`UI_SCALE_MAX`/`UI_ZOOM_CSS_VAR`
- `frontend/src/components/settings/UIScaleSelector.tsx` — Appearance-tab control (preset combobox + manual entry) that calls `setScale`
- `frontend/src/lib/uiScaleSteps.ts` — pure scale-step arithmetic for the Ctrl+wheel HUD: `WHEEL_STEP_PERCENT` (10), `SLIDER_STEP_PERCENT` (5), `roundScaleToStep` (nearest grid multiple, halfway rounds up), `nextScaleOnWheel` (deltaY sign only → snap → step → clamp)
- `frontend/src/hooks/useCtrlWheelScale.ts` — desktop-only modifier+wheel stepping (Cmd on macOS, Ctrl elsewhere; `wails`-gated window listener, capture + non-passive `preventDefault`+`stopPropagation` of the native page-zoom chord) with the transient HUD visibility flag, its 1 s hide timer and the card-grab pause
- `frontend/src/components/uiScale/ScaleHud.tsx` — the transient centered HUD card (Reset + live 5%-step slider + value label), mounted once at the App root (`frontend/src/App.tsx`) in every app phase
- `frontend/src/components/ui/slider.tsx` — the self-styled Radix slider primitive (`components/ui/slider`, built on the `radix-ui` metapackage's `Slider`) used by the HUD: design-token visuals (`bg-secondary` track, `bg-primary` range, `size-4 rounded-full` thumb), no viewport units, scalar `value`/`defaultValue` normalized to the array shape Radix's context requires, and a `thumb` prop threading ARIA onto the announced role=slider thumb
- `frontend/src/index.css` — the `height: 100%` base-sizing chain (`html`/`body`/`#root`) and the zoom-corrected `--ui-vh` length primitive derived from `--ui-zoom`
- `frontend/src/main.tsx` — applies the persisted scale pre-paint (no flash at 100%) and installs the floating-ui compensation before any popover opens
- `frontend/src/lib/layoutSpace.ts` — unit conversion primitives (`getLayoutViewport`, `toLayoutTriggerRect`); carries the measured VISUAL-vs-LAYOUT coordinate model
- `frontend/src/lib/cursorMenuPosition.ts` — pointer-anchored panel placement: pure `computeCursorMenuPlacement` (side choice + clamp) and the `useCursorMenuPosition` hook
- `frontend/src/lib/dropdownPosition.ts` — pure `computeDropdownPosition` (trigger-anchored dropdown flip/shift); unit-agnostic, its callers pass layout-px inputs (via `toLayoutTriggerRect`/`getLayoutViewport`)
- `frontend/src/lib/floatingUiZoom.ts` — global `@floating-ui/dom` platform compensation (`installFloatingUiZoomCompensation`) that makes Radix popovers/tooltips/menus zoom-correct without per-component changes
- `frontend/src/hooks/useResize.ts` — panel-resize divider: `pointerDeltaToLayout` divides pointer deltas by the zoom factor
- `frontend/src/lib/usePanZoom.ts` — canvas pan/zoom (Mermaid/diagram): divides `getBoundingClientRect()` extents by the zoom factor
- `frontend/src/lib/chatScroll.ts` — sticky-aware chat scrolling (`scrollBlockStartIntoView`): divides the `getBoundingClientRect()` delta and the overlay-bar height by the zoom factor before adding them to `scrollTop`
- `frontend/src/components/layout/AppLayout.tsx` — app shell root, sized `h-full w-full` (percentages, never viewport units)
- Guards: `frontend/src/test/zoomViewportInvariant.test.ts`, `frontend/src/lib/layoutSpace.test.ts`, `frontend/src/lib/cursorMenuPosition.test.tsx`, `frontend/src/components/layout/AppLayout.test.tsx`, `frontend/src/stores/uiScaleStore.test.ts`, `frontend/src/lib/floatingUiZoom.test.ts`, `frontend/src/hooks/useResize.test.ts`, `frontend/src/lib/usePanZoom.test.ts`, `frontend/src/lib/chatScroll.test.ts`, `frontend/src/lib/uiScaleSteps.test.ts`, `frontend/src/hooks/useCtrlWheelScale.test.tsx`, `frontend/src/components/uiScale/ScaleHud.test.tsx`

## Behavior

### Applying the scale

The scale is a percent in `[50, 200]`, default `100`, persisted in `localStorage` (`c0wrk-ui-scale`). `setScale` rounds/clamps the value, then `applyScaleToDocument` sets two things on `<html>`:

1. `style.zoom = scale / 100` — the actual magnification;
2. `--ui-zoom = scale / 100` — the same unitless factor as a CSS custom property (so CSS can divide by it inside `calc()`).

It then dispatches a window `resize` so any open floating-ui popover recomputes (a zoom change re-lays-out the document but fires no native `resize`). `main.tsx` performs the same application once before first paint, so the UI never flashes at 100%.

### Ctrl+Wheel scale HUD (desktop-only)

The platform's zoom modifier + wheel is a direct-manipulation input path onto the same scale: **Cmd+wheel on macOS, Ctrl+wheel on Windows/Linux** (`isMacHost` picks the chord; on macOS plain Ctrl is accepted too, because WebKit reports trackpad pinch wheels with the synthetic `ctrlKey` flag). Each wheel notch steps the persisted scale and shows a transient HUD (Reset button + live slider + the current value) centered over the app. Implemented by `useCtrlWheelScale` + `ScaleHud`, mounted once at the App root in every phase (splash screens included — the zoom is an `<html>`-level style and survives phase transitions).

The HUD's slider is the **self-styled Radix slider primitive** (`components/ui/slider.tsx`, built on the `radix-ui` metapackage), not a native `<input type="range">`: the native control is rendered by the platform webview (GTK/WebKit on Linux, a different look on Windows/macOS), so a self-styled control is the only way to render it identically on every OS. All of its visuals come from Tailwind design tokens (track `bg-secondary`, range `bg-primary`, thumb `bg-primary` ring) with absolute px lengths that intentionally scale with the UI zoom; the thumb's percent-based `left` positioning is zoom-safe by the same rule as the HUD's percentage centering. Radix supplies the interaction core (keyboard stepping on the `step` grid, pointer drag, `role=slider` + value ARIA on the thumb); the accessible name and the `%` unit live on the thumb itself (`aria-label`/`aria-valuetext` threaded through the wrapper's `thumb` prop — the root span is not focusable, so a label there is never announced). Its `onValueChange` is array-shaped — the HUD maps `v[0]` onto the scalar store value; a scalar `value`/`defaultValue` input is normalized to `[number]` by the wrapper.

- **Step arithmetic** (`lib/uiScaleSteps`): the current scale is first snapped to the nearest `WHEEL_STEP_PERCENT` (10) grid multiple — halfway rounds up, so repeated notches walk a deterministic grid instead of drifting — then ±10 in the wheel's direction and clamped to `[50, 200]`. Only the **sign** of `deltaY` counts (wheel down = zoom out); trackpad magnitudes must never scale the step, and a zero `deltaY` is a no-op (the snapped current scale — the hook's chord filter drops such events before stepping). The HUD's slider/keyboard steps use the finer `SLIDER_STEP_PERCENT` (5). `setScale` rounds to a whole percent and clamps, so every value written through any path is canonical.
- **Native-zoom suppression**: the listener attaches on `window` in the capture phase and non-passive, and `preventDefault()` runs only for the platform zoom chord — Ctrl+wheel (or Cmd+wheel on macOS) is the browser/webview's own page-zoom gesture — so the viewport never zooms while the app scale steps. The chord is swallowed whole (`stopPropagation`), so canvas-level wheel handlers (`usePanZoom`) never see it — under the chord the wheel means app zoom everywhere. A chord event with `deltaY === 0` (a deltaX-only trackpad gesture or an inertial tail) carries no zoom direction and is ignored untouched. On macOS a Cmd-only wheel outside the chord keeps its native meaning there; off-mac a Cmd-only wheel is ignored untouched. Plain wheel events are ignored untouched and scrolling stays native.
- **HUD lifecycle**: a zoom-chord wheel shows the HUD and **re-arms** the hide timer on every notch, so the deadline is always one `HUD_HIDE_DELAY_MS` = 1000 ms after the last signal — this covers gestures that produce no keyup at all (a trackpad pinch fires synthetic-ctrlKey wheels with no key held). `keyup` Control/Meta or a window `blur` (a blur swallows the keyup after focus loss) re-arms the same way; a `keydown` Control/Meta while visible cancels a pending hide, and a held physical modifier keeps canceling via its auto-repeating keydowns, so holding Cmd/Ctrl through a wheel burst never flickers the HUD away mid-gesture. Pointer interaction with the card pauses the hide: a `pointerdown` on the card holds it (a pending hide is cancelled and cannot re-arm while held — a slider drag outlasts any timer budget), and `pointerup`/`pointercancel` releases and re-arms. While hidden the HUD renders nothing — the steady state owns no UI.
- **Transport gate**: listeners attach only when `transportSnapshot() === 'wails'`. Under `web`, `none` or `pending` no listener is registered at all and the zoom chord keeps its native meaning (dev-server browser, web-access sidecar, tests).
- **Zoom-safe centering**: the HUD wrapper is `fixed inset-0` flex-centered — percentages resolve against the zoom-corrected containing block; no viewport unit, no pointer anchor goes anywhere near `style.left/top`. The wrapper is `pointer-events-none` and only the card re-enables pointer events, so the rest of the UI stays interactive while the HUD is up; hiding is driven by the timer (paused while the card is grabbed), never by clicking away.
- **Store access**: the handlers read the scale via `useUiScaleStore.getState()` (not React state), so the once-per-mount listener set never re-attaches when the scale changes; cleanup removes every listener and cancels a pending hide timer (StrictMode-safe).

### Coordinate model (VISUAL px vs LAYOUT px)

CSS `zoom` pre-multiplies the used value of every `length` while leaving `auto`/percentages untouched. This splits quantities into two spaces (measured in headless Chromium at `html { zoom: Z }`):

| Quantity | Space |
| -------- | ----- |
| `MouseEvent.clientX/clientY` | VISUAL px (`layout × Z`) |
| `Element.getBoundingClientRect()` | VISUAL px |
| `window.innerWidth/innerHeight`, `documentElement.clientWidth/clientHeight` | VISUAL px |
| `style.left/top` on a `position: fixed` element | LAYOUT px |
| `Element.offsetWidth/offsetHeight` | LAYOUT px |
| `transform: translate3d(Npx, …)` | LAYOUT px |
| `100vh` / `100vw` (resolve against the initial containing block) | LAYOUT px |
| `100%` on a fixed element (containing block = `1/Z` box) | LAYOUT px |

`VISUAL px = LAYOUT px × Z`. The visible window in LAYOUT px is therefore `innerWidth / Z × innerHeight / Z`. Any geometry measured in VISUAL px (a pointer point, a trigger rect) must be divided by `Z` before it is written into `style.left/top` or compared against a measured `offsetWidth/offsetHeight`.

### Sizing rule

- The app shell and any full-height container size with **percentages** (`html`/`body`/`#root` carry a `height: 100%` chain; `AppLayout`'s root is `h-full w-full`). A viewport-unit shell (`h-screen`/`w-screen` = `100vh`/`100vw`) is magnified past the window and forces horizontal + vertical scrollbars around the whole app at any scale ≠ 100%.
- Any size that must mean "a fraction of the *visible* viewport" (dialog max-heights, dropdown/tooltip caps, the Mermaid canvas height) goes through the `--ui-vh` custom property — `calc(100vh / var(--ui-zoom, 1))`, defined in `index.css`. E.g. `max-h-[calc(var(--ui-vh)*0.8)]` renders as exactly 80% of the window at any scale and is byte-identical to `max-h-[80vh]` at 100%. Fixed/portaled overlays may equally use a percentage, which resolves against the zoom-corrected initial containing block.
- Absolute lengths (`px`, `rem`) intentionally scale with the UI scale and are left as-is.

### Positioning rule

- **Pointer-anchored panels** (bespoke context menus, cursor menus) route through `lib/cursorMenuPosition`: `useCursorMenuPosition` divides the pointer anchor by the zoom factor, measures the panel with `offsetWidth/offsetHeight`, calls `computeCursorMenuPlacement` (which opens the panel down-right of the cursor by default, flips it left/above when that side has more room, and clamps as a last resort), and returns `position: fixed` `left`/`top` in LAYOUT px. Writing a raw pointer coordinate into `style.left/top` displaces the panel from the cursor by `coordinate × (Z − 1)` and can push it off-screen.
- **Trigger-anchored hand-rolled dropdowns** convert their inputs through `lib/layoutSpace` (`toLayoutTriggerRect` for the target rect, `getLayoutViewport` for the extent) before calling `computeDropdownPosition`.
- **Radix-based popovers** (dropdown menus, tooltips, selects) require no per-component handling: `@floating-ui/dom`'s shared `platform` is patched once by `installFloatingUiZoomCompensation`, which divides the reference rect (`getElementRects`) and the clipping rect (`getClippingRect`) by the zoom factor. `getDimensions` and `convertOffsetParentRelativeRectToViewportRelativeRect` are deliberately left stock (the former already yields layout px via `offsetWidth`; dividing the latter double-compensates).
- **Pointer-driven resizing/panning** divides pointer deltas by the zoom factor (`useResize.pointerDeltaToLayout`, `usePanZoom`).
- **Programmatic scrolling that computes a `scrollTop` from measured geometry** divides the `getBoundingClientRect()` delta (and any rect-derived overlay height) by the zoom factor before adding it to `scrollTop` (`chatScroll.scrollBlockStartIntoView`): rect geometry is VISUAL px while `scrollTop` is LAYOUT px, so a raw delta overshoots by `delta × (Z − 1)` at any scale ≠ 100%.

### Why a global patch for floating-ui

floating-ui measures references with `getBoundingClientRect()` (VISUAL px) and returns coordinates the caller writes into `style.left/top` (LAYOUT px). Its own `getScale`/`includeScale` handling cannot fix this for Radix portaled content using `strategy: 'fixed'` (the floating element's offset parent resolves to the Window, so the reference rect is never divided). Mutating the single shared `platform` object is what covers Radix, `@floating-ui/react-dom`, and `@floating-ui/core` at once; the package is deduplicated to one copy in `node_modules`.

## Error Handling

- **Corrupted persisted scale**: `getUiZoomFactor` returns `1` for a non-finite or non-positive scale, so geometry is never divided by garbage; `normalizeScale` clamps out-of-range stored/entered values to `[50, 200]`.
- **No DOM (tests/SSR)**: `applyScaleToDocument` and `dispatchUiResizeNotification` are no-ops when `document`/`window` is unavailable.
- **Non-wails transports**: `useCtrlWheelScale` attaches no listeners when `transportSnapshot()` is not `wails` (`web`, `none`, `pending`), so the zoom chord keeps its native meaning in the dev-server browser, the web-access sidecar and tests, and no hide timer can be armed.
- **Panel larger than the viewport**: `computeCursorMenuPlacement` pins the leading edge at the gutter (nothing can fully fit), keeping the visible part usable.
- **Compensation idempotency**: `installFloatingUiZoomCompensation` marks wrapped methods and never re-wraps; at zoom 1 every compensation is an identity.

## Invariants

- The UI scale lives only in `uiScaleStore`; the applied `zoom` and the `--ui-zoom` custom property on `<html>` are its sole projection (written by `applyScaleToDocument`, which is idempotent and safe to call repeatedly).
- The app shell and every full-height container always size with percentages (`height: 100%` chain + `h-full w-full`), never with `100vh`/`100vw` or the `*-screen` utilities.
- Every viewport-derived size is expressed through `--ui-vh` (or a percentage on a fixed element), never a raw viewport unit.
- Every pointer-anchored floating panel derives its `left`/`top` from `lib/cursorMenuPosition` (or, for a trigger-anchored dropdown, `lib/layoutSpace`), so it opens under the cursor and entirely inside the visible window at any scale; Radix popovers rely on the global floating-ui compensation instead.
- Pointer deltas that feed layout-px sizes (resize dividers, canvas pan/zoom) are divided by the zoom factor.
- The zoom chord (Ctrl+wheel, Cmd+wheel on macOS) is intercepted only while its modifier is held and only under the `wails` transport (capture-phase, non-passive listener that swallows the chord — `preventDefault` + `stopPropagation`; plain wheel and zero-`deltaY` chord events are never touched). Wheel steps always land on the 10% grid (snap → step → clamp), HUD slider/keyboard steps on the 5% grid, and `setScale` persists only whole percents within `[50, 200]`.
- The scale HUD is a passive overlay: its wrapper lets every event through (`pointer-events-none`, only the card re-enables pointer events), it is centered with percentages (never viewport units, never a pointer coordinate), and it exists only while a gesture is active — every appearance re-arms the same 1 s hide timer (paused while the card is grabbed, released by `pointerup`/`pointercancel`), and unmount removes every listener and cancels any pending timer.
- The HUD's slider is always the self-styled Radix primitive (`components/ui/slider`), never a native `<input type="range">` — a native control's rendering depends on the platform webview, which defeats the identical-across-OS requirement; its visuals come from design tokens only, and its percent-based thumb positioning is zoom-safe.
- At scale `100` every compensation is an identity, so behaviour is unchanged from a zoom-free build.
- The invariant is enforced by tests: `frontend/src/test/zoomViewportInvariant.test.ts` is a project-wide source scan that fails on any raw viewport unit, `*-screen` utility, or `left`/`top` fed from a raw `.x`/`.y`/`clientX`/`clientY` anchor; the remaining guard files pin the shell sizing, the geometry helpers, the store, the floating-ui patch, and the resize/pan hooks.

## Extension Points

- **New pointer-anchored panel** (context menu, cursor menu): use `useCursorMenuPosition`; render the panel with `visibility: hidden` until the hook returns a placement (it measures in a layout effect, so the hidden state is never painted).
- **New trigger-anchored dropdown** not built on Radix: pass layout-px inputs (`toLayoutTriggerRect`, `getLayoutViewport`) to `computeDropdownPosition`.
- **New viewport-sized element**: size it with `--ui-vh` (or a percentage), never a raw `vh`/`vw`.
- **New interactive geometry** reading `getBoundingClientRect()`/pointer coordinates into a layout-px style or size: divide by `getUiZoomFactor()` (or reuse `lib/layoutSpace`).
- **New scale input surface** (hotkey, menu item, another gesture): reuse `lib/uiScaleSteps` for the grid/clamp arithmetic and land the value through `setScale` — never write `style.zoom` or `--ui-zoom` directly.
- **New guard**: extend `zoomViewportInvariant.test.ts` when a new class of zoom-sensitive pattern is introduced.

## Related Specs

- [stores.md](stores.md) — `uiScaleStore` in the store catalog
- [README.md](README.md) — frontend architecture overview
- [rendering.md](rendering.md) — message display pipeline (consumer of zoom-safe layout)
- [../../architecture/layers.md](../../architecture/layers.md) — frontend layer boundaries
