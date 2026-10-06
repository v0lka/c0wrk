import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { ErrorBoundary } from './components/ErrorBoundary'
import { registerLanguages } from './lib/hljsLanguages'
import '@xterm/xterm/css/xterm.css'
import './index.css'
import { useThemeStore, applyThemeToDocument, selectActiveThemeType } from './stores/themeStore'
import { useUiScaleStore, applyScaleToDocument } from './stores/uiScaleStore'
import {
  useFontStore,
  applyFontsToDocument,
  removeOrphanSystemFontKey,
} from './stores/fontStore'
import { installFloatingUiZoomCompensation } from './lib/floatingUiZoom'

// Apply the persisted theme before first paint to avoid a flash of the
// default (dark) theme. Accessing getState() rehydrates from localStorage
// synchronously; applyThemeToDocument writes <html data-theme> so the CSS
// token override is in place before React renders anything. For a custom
// theme this also injects the cached theme CSS (persisted alongside the id)
// into <head> — the palette is correct on the very first frame, no flash of
// the built-in palette while the backend catalog loads.
applyThemeToDocument(
  useThemeStore.getState().themeId,
  useThemeStore.getState().themeCss,
  selectActiveThemeType(useThemeStore.getState()),
)

// Same first-paint contract as the theme above: apply the persisted UI scale
// (<html style="zoom">) before React renders, so the layout never flashes at
// 100% for users who changed the zoom level.
applyScaleToDocument(useUiScaleStore.getState().scale)

// Same first-paint contract as theme and scale: the persisted font choices
// land as inline --font-sans/--font-mono overrides on <html> before React
// renders, so text never flashes with the default stacks. getState()
// rehydrates from localStorage synchronously. The replaced follow-system-font
// store's key is retired here too — its boolean payload has no successor in
// the chosen-family model, so it must not linger (and its store no longer
// exists to rewrite it).
removeOrphanSystemFontKey()
applyFontsToDocument(
  useFontStore.getState().uiFontFamily,
  useFontStore.getState().monoFontFamily,
)

// Patch @floating-ui/dom's shared platform so popovers/tooltips/menus
// position correctly under the zoom: floating-ui measures references in
// visual px (getBoundingClientRect) but the caller writes its result into
// style.left/top in layout px. Must run before any popover opens; the
// wrappers read the live zoom factor on every call.
installFloatingUiZoomCompensation()

registerLanguages()

// Prevent text selection on double-click / triple-click only inside containers
// that opt in via [data-no-select]. The previous global behavior also blocked
// word/line selection in read-only content like markdown viewers and code
// blocks where users expect double-click selection to work. (W-32)
//
// MouseEvent.detail > 1 means the mousedown is the 2nd (double) or 3rd (triple)
// click of a rapid sequence — the browser default is to select the word or
// paragraph. Calling preventDefault() stops that selection while leaving
// drag-to-select untouched (drag always starts with detail === 1).
document.addEventListener('mousedown', (e: MouseEvent) => {
  if (e.detail <= 1) return
  const t = e.target as HTMLElement | null
  if (!t) return
  if (
    t instanceof HTMLInputElement ||
    t instanceof HTMLTextAreaElement ||
    t.isContentEditable
  ) {
    return
  }
  // Only suppress when an ancestor explicitly opts in via [data-no-select].
  // closest() walks up to the document root, so any wrapping container can
  // disable double-click selection for itself and its descendants.
  if (!t.closest('[data-no-select]')) return
  e.preventDefault()
})

const root = document.getElementById('root')
if (!root) throw new Error('Root element not found')

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </React.StrictMode>,
)
