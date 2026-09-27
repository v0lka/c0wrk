// Shared harness for the two embedded-LLM tuning suites
// (EmbeddedLLMTuning.test.tsx / EmbeddedLLMAdvancedTuning.test.tsx).
//
// Both suites drive their section through the REAL useEmbeddedLLMTuning hook and
// the REAL store, with the RPC boundary mocked at @/api/embeddedTuning and a
// mini fold so a committed patch round-trips through the re-read exactly as the
// backend would. Everything that is not a `vi.mock` block lives here — the mocks
// MUST stay per-file, since vitest hoists them.
//
// The interaction helpers are the point of the extraction: `touchField`,
// `retypeField`, `emptyField`, `enterField`, `blurField` and `typeField` are the
// ONLY executable specification of what counts as "an interaction a real user
// produces" for the shared NumberField, and they pin its
// edit-dirty-vs-value-comparison contract. A second copy would mean fixing that
// model twice, and a drifting one would silently weaken the other suite.
//
// They compose into the gestures that matter, and the composition is NOT
// interchangeable: `touchField` is focus+blur (a tab-through), while
// `enterField` + `blurField` is Enter-then-click-away (the DOM node never lost
// focus, so no `focusin` may be dispatched in between — one would clear the
// edit-dirty flag and make the "one gesture, one persist" tests unfalsifiable).
// `typeField` / `focusField` are the keystroke and re-focus primitives those two
// are built from, exported so a test can split a gesture at the exact point it
// wants to assert on.
//
// `container` is a live ES-module binding (assigned by `mountHarness`) so the
// suites query the DOM directly instead of threading the element everywhere.

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TuningHarnessSection } from './TuningHarnessSection'
import { focusIn, focusOut, pressKey, typeValue } from './domNumberInput'
import { refreshEmbeddedLLMTuning, useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import { makeColdEmbeddedStatus, makeEmbeddedStatus } from './embeddedStatusFixture'
import type { EmbeddedLLMStatus } from '@/api/embedded'

// The tuning fixtures live in ./embeddedTuningFixture (shared with
// EmbeddedLLMSettings.test.tsx) and are re-exported here, so both section
// suites import everything they need from this one module.
export { applyPatch, makeTuning } from './embeddedTuningFixture'

/** Which section the harness renders. */
export type TuningHarnessVariant = 'primary' | 'advanced'

/** The live container the harness mounts into. Assigned by `mountHarness`. */
export let container!: HTMLDivElement

let root: Root | null = null
let variant: TuningHarnessVariant = 'primary'

// --- Mount lifecycle ---

/** Create the container and the React root for one test. */
export function mountHarness(next: TuningHarnessVariant): void {
  variant = next
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
}

/** Unmount and drop the container. Idempotent. */
export function unmountHarness(): void {
  const active = root
  if (active === null) return
  root = null
  act(() => {
    active.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
}

/** Flush pending microtasks inside act() so async state updates settle. */
export async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Mount the section and let the first tuning read land. */
export async function render(): Promise<void> {
  const active = root
  if (active === null) throw new Error('mountHarness() was not called')
  await act(async () => {
    active.render(<TuningHarnessSection variant={variant} />)
  })
  await flush()
}

/** A fresh authoritative tuning read (what a commit or an event triggers). */
export async function reloadTuning(): Promise<void> {
  await act(async () => {
    await refreshEmbeddedLLMTuning()
  })
  await flush()
}

// --- Status seeding ---

/** A status seed. `plan` is merged into the fixture's unrecorded defaults, so a
 *  test names only the launch-shape fields it asserts on. */
export type StatusSeed = Omit<Partial<EmbeddedLLMStatus>, 'plan'> & {
  plan?: Partial<EmbeddedLLMStatus['plan']>
}

/** Seed the store's status snapshot (reload_required, the recorded plan, …).
 *  The base is an installed-but-stopped model. */
export function seedStatus(seed: StatusSeed = {}): void {
  const { plan, ...rest } = seed
  act(() => {
    useEmbeddedLLMStore.getState().setStatus(
      makeColdEmbeddedStatus({
        ...rest,
        ...(plan ? { plan: { ...makeEmbeddedStatus().plan, ...plan } } : {}),
      }),
    )
  })
}

// --- DOM queries and interactions ---

/** One NumberField input, by its label. */
export function field(label: string): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>(`input[data-field="${label}"]`)
  if (!el) throw new Error(`field ${label} not found`)
  return el
}

/** One combobox trigger, by its accessible name. */
export function trigger(ariaLabel: string): HTMLButtonElement {
  const el = Array.from(
    container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]'),
  ).find((b) => b.getAttribute('aria-label') === ariaLabel)
  if (!el) throw new Error(`trigger ${ariaLabel} not found`)
  return el
}

/** Open the combobox with `ariaLabel` and click its `optionLabel` option. */
export async function pickOption(ariaLabel: string, optionLabel: string): Promise<void> {
  const t = trigger(ariaLabel)
  await act(async () => {
    t.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const option = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
    (o) => o.textContent?.trim() === optionLabel,
  )
  if (!option) throw new Error(`Option "${optionLabel}" not found in ${ariaLabel} menu`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

// The event primitives live in ./domNumberInput (ONE copy, shared with the Model
// Profiles suite); the harness adds the label lookup and the `flush()` that lets
// a commit's RPC + re-read land before the caller asserts.

async function focus(label: string): Promise<HTMLInputElement> {
  const input = field(label)
  await focusIn(input)
  return input
}

async function blur(input: HTMLInputElement): Promise<void> {
  await focusOut(input)
  await flush()
}

/** Focus + type the WHOLE value in one input event + blur. */
export async function editField(label: string, value: string): Promise<void> {
  const input = await focus(label)
  typeValue(input, value)
  await blur(input)
}

/** Focus + blur a NumberField WITHOUT typing — the "the user tabbed through"
 *  case. A blur here must persist nothing: the value on screen may be a
 *  fallback, not a stored override. */
export async function touchField(label: string): Promise<void> {
  await blur(await focus(label))
}

/** Focus a NumberField WITHOUT typing and WITHOUT blurring — the `focusin` half
 *  of a gesture, exported so a test can state "the user clicked back into the
 *  field" as its own step and then type into it (see `typeField`). */
export async function focusField(label: string): Promise<HTMLInputElement> {
  return focus(label)
}

/** Dispatch ONLY `focusout` — a bare blur with NO preceding `focusin`.
 *
 *  This is the second half of the REAL Enter-then-click-away gesture: the DOM
 *  node never lost focus, so no `focus` event fires again before the blur.
 *  `touchField` cannot express it — its `focusin` runs `NumberField.onFocus`,
 *  which re-seeds the draft and clears the edit-dirty flag, so the flag would be
 *  consumed by the re-focus rather than by `commit`. Any test that means to pin
 *  "one gesture, one persist" MUST use this helper instead. */
export async function blurField(label: string): Promise<void> {
  await blur(field(label))
}

/** Type into a field that ALREADY has focus — one `input` event, no focus event
 *  on either side. The only way to express "one more keystroke after an Enter",
 *  because Enter commits without moving the DOM focus. */
export async function typeField(label: string, text: string): Promise<void> {
  typeValue(field(label), text)
  await flush()
}

/** Retype a NumberField the way a user does: select-all, then one keystroke per
 *  character. Every intermediate value differs from the one before it, so each
 *  raises a change event — including the last, which restores the text the field
 *  already showed. This is the "deliberately typed the fallback" case that a
 *  `parsed !== value` guard would wrongly refuse and an edit-dirty check accepts. */
export async function retypeField(label: string, text: string): Promise<void> {
  const input = await focus(label)
  typeValue(input, '')
  for (let i = 1; i <= text.length; i += 1) typeValue(input, text.slice(0, i))
  await blur(input)
}

/** Focus + select-all + Backspace (ONE input event carrying the empty string —
 *  which is also what `<input type="number">` reports for a half-typed `1e`),
 *  then blur. The field was edited, so the dirty flag is set: the commit must
 *  still persist nothing, because an empty draft means "no value", not 0. */
export async function emptyField(label: string): Promise<void> {
  const input = await focus(label)
  typeValue(input, '')
  await blur(input)
}

/** Focus + optionally type `text` + press Enter, WITHOUT blurring — the gesture
 *  the sibling auto-unload minutes field has always accepted. `commit` consumes
 *  the dirty flag and leaves the focus flag alone (the DOM node keeps focus:
 *  `type="number"` outside a `<form>` gives Enter no default action), so a later
 *  bare blur is a no-op unless the user typed something new: one gesture, one
 *  persist. */
export async function enterField(label: string, text?: string): Promise<void> {
  const input = await focus(label)
  if (text !== undefined) typeValue(input, text)
  await pressKey(input, 'Enter')
  await flush()
}
