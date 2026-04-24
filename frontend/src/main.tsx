import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { ErrorBoundary } from './components/ErrorBoundary'
import { registerLanguages } from './lib/hljsLanguages'
import '@xterm/xterm/css/xterm.css'
import './index.css'

registerLanguages()

// Prevent text selection on double-click / triple-click across the entire UI.
// MouseEvent.detail > 1 means the mousedown is the 2nd (double) or 3rd (triple)
// click of a rapid sequence — the browser default is to select the word or
// paragraph.  Calling preventDefault() stops that selection while leaving
// drag-to-select untouched (drag always starts with detail === 1).
// Form elements (input, textarea, contentEditable) keep native double-click
// selection because users expect it inside text fields.
document.addEventListener('mousedown', (e: MouseEvent) => {
  if (e.detail <= 1) return
  const t = e.target as HTMLElement
  if (
    t instanceof HTMLInputElement ||
    t instanceof HTMLTextAreaElement ||
    t.isContentEditable
  ) {
    return
  }
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
