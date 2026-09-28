// NumberField — the shared integer/decimal input for thresholds and counts.
//
// Extracted from ModelProfilesControls because it outgrew that module's
// concern: it is now a cross-feature primitive (Model Profiles' loop-hardening
// and context knobs, and every embedded-LLM tuning knob) with a commit contract
// of its own, and the two embedded tuning suites treat its blur semantics as
// the executable specification of "an interaction a real user produces".

import { useRef, useState } from 'react'
import { Input } from '@/components/ui/input'

/**
 * NumberField — numeric input for thresholds / counts. Persists on blur or
 * Enter to avoid a save storm while typing. `min`/`max` are the inclusive
 * bounds; an out-of-range entry is rejected (the field reverts) so the value
 * committed here always matches the backend's accepted range.
 *
 * A commit runs on an EDIT, never on a bare focus loss: the field tracks
 * whether its text changed since it gained focus and calls `onChange` only
 * then. That is load-bearing wherever `value` is a rendered FALLBACK rather
 * than a stored one — the embedded-LLM tuning knobs, whose unset state means
 * "the planner decides" — because committing on a plain blur would pin the
 * fallback as an override nobody chose (`cache_ram_mib: 0` DISABLES the prompt
 * cache, `-ngl 0` is an all-CPU launch shape). An edit-dirty flag, not a
 * `parsed !== value` comparison, keeps both properties true: a value the user
 * deliberately typed still commits when it happens to equal the fallback.
 *
 * An EMPTIED field is "no value", never zero: `ToNumber('')` is +0, so an empty
 * draft would otherwise pass `Number.isFinite` and a `min` of 0 and commit a 0
 * the operator never typed. `<input type="number">` reports `''` for any
 * not-yet-valid content too (a half-typed `1e` on the way to `1e3`), so this is
 * reachable without select-all+Backspace. Emptying reverts the field and
 * persists nothing — the same outcome as the out-of-range branch — which keeps
 * `0` reachable by typing `0`.
 *
 * That empty check needs NO `.trim()`, and adding one would be dead code:
 * `draft` is only ever assigned `e.target.value`, `String(value)` or
 * `String(parsed)`, and the number-state value sanitization algorithm replaces
 * any `e.target.value` that is not a valid floating-point number with `''` —
 * and a valid floating-point number cannot carry surrounding ASCII whitespace.
 * A whitespace-only draft therefore reaches `commit` as the empty draft and
 * takes the very same branch; there is no `'  '` for a trim to catch.
 *
 * `integer` rejects a non-integer entry the same way (the field reverts, nothing
 * persists). It is set for every knob whose wire type is an `int`, because
 * `<input type="number">`'s default `step=1` constrains only the SPINNER
 * buttons — typed text is unconstrained, so `2.5` would otherwise travel all the
 * way to Go and come back as a raw `json: cannot unmarshal number 2.5 into Go
 * struct field … of type int` painted in the surface's error line.
 *
 * Enter commits exactly like blur does (the sibling auto-unload minutes field
 * has always done this, and `type="number"` outside a `<form>` gives Enter no
 * default action of its own) — but it does NOT clear the focus flag, because
 * the DOM node KEEPS focus: with no default action there is no navigation and no
 * blur, so a `focused` of false would disagree with the DOM. The field would
 * then render `String(value)` while every further keystroke still landed in
 * `draft` and re-armed the dirty flag — text the user never sees, persisted by
 * the eventual real blur. Instead Enter keeps the field in its editing state and
 * re-seeds `draft` from the commit's own outcome, so the rendered text is ALWAYS
 * the text a commit just accepted (or the value it rejected). A following blur
 * is a no-op unless the user genuinely typed something new after the Enter: one
 * gesture, one persist.
 */
interface NumberFieldProps {
  label: string
  value: number
  onChange: (value: number) => void
  min?: number
  max?: number
  step?: number
  /** Reject a non-integer entry (the field reverts). Set for `int`-typed knobs. */
  integer?: boolean
  disabled?: boolean
}

export function NumberField({
  label,
  value,
  onChange,
  min,
  max,
  step,
  integer,
  disabled,
}: NumberFieldProps) {
  const [draft, setDraft] = useState(String(value))
  const [focused, setFocused] = useState(false)
  // Edit-dirty: the text was actually changed since the field gained focus. A
  // ref, not state — nothing renders from it, and the commit handler (blur or
  // Enter) then reads the current flag instead of one render's snapshot of it.
  const edited = useRef(false)

  const display = focused ? draft : String(value)

  // `keepFocus` is the Enter path: the DOM node retains focus, so the flag must
  // too (see the contract comment). The blur path clears it, which is what makes
  // `display` fall back to the authoritative `value`.
  const commit = (keepFocus: boolean) => {
    if (!keepFocus) setFocused(false)
    // No keystroke since focus: `draft` is only the `value` this focus
    // re-seeded it from, which may be a fallback. Committing it would invent an
    // override the user never typed.
    if (!edited.current) return
    edited.current = false
    // An emptied field means "no value", not 0 — `Number('')` is +0, which would
    // pass a `min` of 0. Every rejected branch reverts to `value` below. No
    // `.trim()`: the number input already sanitized a whitespace-only draft to
    // `''` (see the contract comment), so `draft` never carries one.
    const text = draft
    const parsed = Number(text)
    const accepted =
      text !== '' &&
      Number.isFinite(parsed) &&
      (!integer || Number.isInteger(parsed)) &&
      (min === undefined || parsed >= min) &&
      (max === undefined || parsed <= max)
    // Re-seed the draft from the outcome, on BOTH paths: while `focused` is true
    // (the Enter path) `display` reads `draft`, so leaving the rejected or
    // normalized text there is exactly the divergence this guards against.
    setDraft(accepted ? String(parsed) : String(value))
    if (accepted) onChange(parsed)
  }

  return (
    <div className="flex flex-col gap-1">
      <label className="text-xs text-muted-foreground">{label}</label>
      <Input
        type="number"
        data-field={label}
        value={display}
        disabled={disabled}
        min={min}
        max={max}
        step={step}
        onFocus={() => {
          setFocused(true)
          setDraft(String(value))
          edited.current = false
        }}
        onChange={(e) => {
          setDraft(e.target.value)
          edited.current = true
        }}
        onBlur={() => commit(false)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit(true)
        }}
        className="h-8 w-24 text-sm"
      />
    </div>
  )
}
