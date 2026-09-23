# UI Scale (Frontend Zoom-Safety)

## Role

Defines the app-wide UI scale feature (the "UI Scale" setting) and the **zoom-safety invariant** every frontend change must uphold: the scale is applied as CSS `zoom` on `<html>`, which changes the meaning of `length` values and the coordinate space of pointer geometry, so sizing and popover placement must be written in a zoom-aware way.

## Key Files

- `frontend/src/stores/uiScaleStore.ts` — scale state (`50`–`200`%, default `100`, persisted under `c0wrk-ui-scale`), `setScale` (normalize → clamp → apply → notify), `applyScaleToDocument` (writes `style.zoom` and the `--ui-zoom` custom property on `<html>`), `getUiZoomFactor` (live factor for non-React code), `dispatchUiResizeNotification` (window `resize` so open floating-ui popovers recompute), `UI_SCALE_MIN`/`UI_SCALE_MAX`/`UI_ZOOM_CSS_VAR`
- `frontend/src/components/settings/UIScaleSelector.tsx` — Appearance-tab control (preset combobox + manual entry) that calls `setScale`
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
- Guards: `frontend/src/test/zoomViewportInvariant.test.ts`, `frontend/src/lib/layoutSpace.test.ts`, `frontend/src/lib/cursorMenuPosition.test.tsx`, `frontend/src/components/layout/AppLayout.test.tsx`, `frontend/src/stores/uiScaleStore.test.ts`, `frontend/src/lib/floatingUiZoom.test.ts`, `frontend/src/hooks/useResize.test.ts`, `frontend/src/lib/usePanZoom.test.ts`, `frontend/src/lib/chatScroll.test.ts`

## Behavior

### Applying the scale

The scale is a percent in `[50, 200]`, default `100`, persisted in `localStorage` (`c0wrk-ui-scale`). `setScale` rounds/clamps the value, then `applyScaleToDocument` sets two things on `<html>`:

1. `style.zoom = scale / 100` — the actual magnification;
2. `--ui-zoom = scale / 100` — the same unitless factor as a CSS custom property (so CSS can divide by it inside `calc()`).

It then dispatches a window `resize` so any open floating-ui popover recomputes (a zoom change re-lays-out the document but fires no native `resize`). `main.tsx` performs the same application once before first paint, so the UI never flashes at 100%.

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
- **Panel larger than the viewport**: `computeCursorMenuPlacement` pins the leading edge at the gutter (nothing can fully fit), keeping the visible part usable.
- **Compensation idempotency**: `installFloatingUiZoomCompensation` marks wrapped methods and never re-wraps; at zoom 1 every compensation is an identity.

## Invariants

- The UI scale lives only in `uiScaleStore`; the applied `zoom` and the `--ui-zoom` custom property on `<html>` are its sole projection (written by `applyScaleToDocument`, which is idempotent and safe to call repeatedly).
- The app shell and every full-height container always size with percentages (`height: 100%` chain + `h-full w-full`), never with `100vh`/`100vw` or the `*-screen` utilities.
- Every viewport-derived size is expressed through `--ui-vh` (or a percentage on a fixed element), never a raw viewport unit.
- Every pointer-anchored floating panel derives its `left`/`top` from `lib/cursorMenuPosition` (or, for a trigger-anchored dropdown, `lib/layoutSpace`), so it opens under the cursor and entirely inside the visible window at any scale; Radix popovers rely on the global floating-ui compensation instead.
- Pointer deltas that feed layout-px sizes (resize dividers, canvas pan/zoom) are divided by the zoom factor.
- At scale `100` every compensation is an identity, so behaviour is unchanged from a zoom-free build.
- The invariant is enforced by tests: `frontend/src/test/zoomViewportInvariant.test.ts` is a project-wide source scan that fails on any raw viewport unit, `*-screen` utility, or `left`/`top` fed from a raw `.x`/`.y`/`clientX`/`clientY` anchor; the remaining guard files pin the shell sizing, the geometry helpers, the store, the floating-ui patch, and the resize/pan hooks.

## Extension Points

- **New pointer-anchored panel** (context menu, cursor menu): use `useCursorMenuPosition`; render the panel with `visibility: hidden` until the hook returns a placement (it measures in a layout effect, so the hidden state is never painted).
- **New trigger-anchored dropdown** not built on Radix: pass layout-px inputs (`toLayoutTriggerRect`, `getLayoutViewport`) to `computeDropdownPosition`.
- **New viewport-sized element**: size it with `--ui-vh` (or a percentage), never a raw `vh`/`vw`.
- **New interactive geometry** reading `getBoundingClientRect()`/pointer coordinates into a layout-px style or size: divide by `getUiZoomFactor()` (or reuse `lib/layoutSpace`).
- **New guard**: extend `zoomViewportInvariant.test.ts` when a new class of zoom-sensitive pattern is introduced.

## Related Specs

- [stores.md](stores.md) — `uiScaleStore` in the store catalog
- [README.md](README.md) — frontend architecture overview
- [rendering.md](rendering.md) — message display pipeline (consumer of zoom-safe layout)
- [../../architecture/layers.md](../../architecture/layers.md) — frontend layer boundaries
