import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { ErrorBoundary } from './components/ErrorBoundary'
import { registerLanguages } from './lib/hljsLanguages'
import '@xterm/xterm/css/xterm.css'
import './index.css'

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
