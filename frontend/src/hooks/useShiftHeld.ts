import { useSyncExternalStore } from 'react'

// Shared module-level Shift-key state behind a single set of window
// listeners. Every chat message renders its own CopyButton, so per-button
// listeners would multiply across the message list — one external store keeps
// the cost constant no matter how many buttons subscribe.
let shiftHeld = false
const listeners = new Set<() => void>()
let attached = false

function notify(): void {
  for (const listener of listeners) listener()
}

function handleKeyDown(event: KeyboardEvent): void {
  if (event.key === 'Shift' && !shiftHeld) {
    shiftHeld = true
    notify()
  }
}

function handleKeyUp(event: KeyboardEvent): void {
  if (event.key === 'Shift' && shiftHeld) {
    shiftHeld = false
    notify()
  }
}

// Releasing Shift after the window lost focus produces no keyup — reset on
// blur so the buttons never get stuck showing the Shift-variant action.
function handleBlur(): void {
  if (shiftHeld) {
    shiftHeld = false
    notify()
  }
}

function attach(): void {
  if (attached || typeof window === 'undefined') return
  window.addEventListener('keydown', handleKeyDown)
  window.addEventListener('keyup', handleKeyUp)
  window.addEventListener('blur', handleBlur)
  attached = true
}

function detach(): void {
  if (!attached) return
  window.removeEventListener('keydown', handleKeyDown)
  window.removeEventListener('keyup', handleKeyUp)
  window.removeEventListener('blur', handleBlur)
  attached = false
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  attach()
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) detach()
  }
}

function getSnapshot(): boolean {
  return shiftHeld
}

/**
 * useShiftHeld — reactive global Shift-key state.
 *
 * Returns true while either Shift key is held, false otherwise (including
 * after a window blur that swallowed the keyup). Used by message action
 * buttons to swap their action while a modifier is held (Copy → Save).
 */
export function useShiftHeld(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot)
}
