
import { useState } from 'react'
import { Input } from '@/components/ui/input'

interface OptionalNumberFieldProps {
  label: string
  /** Current numeric value. 0 means "inherit the vendor preset". */
  value: number
  onChange: (value: number) => void
  /** Lower bound applied only to explicit (non-empty) input. */
  min?: number
  /** Upper bound applied only to explicit (non-empty) input. */
  max?: number
  step?: number
  placeholder?: string
  hint?: string
  disabled?: boolean
}

/**
 * Numeric field with inherit semantics: an empty input maps to 0, which the
 * backend treats as "inherit the vendor default sampling preset". The
 * placeholder advertises the vendor default so the inherit state is visible.
 */
export function OptionalNumberField({
  label,
  value,
  onChange,
  min,
  max,
  step,
  placeholder,
  hint,
  disabled,
}: OptionalNumberFieldProps) {
  const [draft, setDraft] = useState(value > 0 ? String(value) : '')
  const [focused, setFocused] = useState(false)
  const display = focused ? draft : value > 0 ? String(value) : ''

  const commit = () => {
    setFocused(false)
    const trimmed = draft.trim()
    if (trimmed === '') {
      setDraft('')
      onChange(0)
      return
    }
    const parsed = Number(trimmed)
    const withinBounds =
      Number.isFinite(parsed) &&
      (min === undefined || parsed >= min) &&
      (max === undefined || parsed <= max)
    if (withinBounds) {
      setDraft(String(parsed))
      onChange(parsed)
    } else {
      setDraft(value > 0 ? String(value) : '')
    }
  }

  return (
    <div className="flex flex-col gap-1" data-testid={`optional-number-${label}`}>
      <label className="text-xs text-muted-foreground">{label}</label>
      <Input
        type="number"
        data-field={label}
        value={display}
        disabled={disabled}
        min={min}
        max={max}
        step={step}
        placeholder={placeholder}
        onFocus={() => {
          setFocused(true)
          setDraft(value > 0 ? String(value) : '')
        }}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        className="h-8 w-24 text-sm"
      />
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}
