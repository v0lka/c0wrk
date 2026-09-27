// Low-level DOM event primitives for driving a controlled `<input>` under
// jsdom, shared by every suite that types into one.
//
// React attaches its synthetic handlers at the root, so a test has to dispatch
// the SAME events a browser does. Two jsdom gaps make the raw APIs unusable
// here: `HTMLElement.focus()`/`blur()` fire nothing for an element that was
// never `document.activeElement`, and assigning `.value` directly is ignored by
// a controlled input because React compares against its own value tracker — the
// native setter has to be invoked explicitly for the `input` event to register.
//
// ONE copy on purpose (the same reason @/test/tuningTestHarness exists): "what
// counts as an interaction a real user produces" must not be defined twice, or a
// drifting copy silently weakens the contract the other suite pins.
//
// Renderer-free and store-free — every function takes the element it acts on, so
// a caller composes them into whatever gesture it means to express.

import { act } from 'react'

/** The native value setter React's change tracking compares against. */
const inputValueSetter = (): ((v: string) => void) =>
  Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!

/** Dispatch `focusin` — the event React maps to `onFocus`. */
export async function focusIn(input: HTMLInputElement): Promise<void> {
  await act(async () => {
    input.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    await Promise.resolve()
  })
}

/** Dispatch `focusout` ONLY — the event React maps to `onBlur`, with no
 *  preceding `focusin`. This is the second half of the real
 *  Enter-then-click-away gesture: the DOM node never lost focus, so the browser
 *  fires no `focus` event in between. A helper that re-focused first would let
 *  `onFocus` run, which is a DIFFERENT gesture with different consequences. */
export async function focusOut(input: HTMLInputElement): Promise<void> {
  await act(async () => {
    input.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Set the value the way a keystroke does and raise one `input` event. */
export function typeValue(input: HTMLInputElement, text: string): void {
  act(() => {
    inputValueSetter().call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/** Dispatch a `keydown` for `key` and let the handler's state updates settle. */
export async function pressKey(input: HTMLInputElement, key: string): Promise<void> {
  await act(async () => {
    input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })
}
