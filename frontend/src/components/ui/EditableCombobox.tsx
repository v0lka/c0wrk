import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { CheckIcon, ChevronDownIcon } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

interface EditableComboboxProps {
  /** Current numeric value (percent, tokens, …). */
  value: number
  /** Preset choices offered in the dropdown. */
  presets: readonly number[]
  /** Inclusive bounds applied on commit (Enter / blur / preset pick). */
  min: number
  max: number
  /** Suffix rendered next to the number (e.g. "%"). */
  unit?: string
  onChange: (n: number) => void
  /** Accessible name for the text input and the chevron button. */
  ariaLabel: string
  disabled?: boolean
  className?: string
}

/**
 * EditableCombobox — a numeric input with a preset dropdown for settings
 * forms, built on the project's Radix `dropdown-menu` primitives (works
 * inside modal Radix dialogs for the same reasons as `Combobox`).
 *
 * The text field is the primary input; the chevron button (or ArrowDown /
 * Alt+ArrowDown) opens a Radix DropdownMenu of presets. Local text state
 * mirrors `value` until the user types; commits happen on Enter, blur or
 * preset pick with `parseInt` semantics: NaN or empty reverts to the last
 * valid value (no onChange), out-of-range values are clamped to [min, max].
 * Escape reverts the pending edit without committing.
 */
export function EditableCombobox({
  value,
  presets,
  min,
  max,
  unit,
  onChange,
  ariaLabel,
  disabled = false,
  className,
}: EditableComboboxProps) {
  const [text, setText] = useState(String(value))
  const [open, setOpen] = useState(false)
  const lastValid = useRef(value)

  // Keep the field in sync when the controlled value changes externally
  // (preset picked elsewhere, settings reloaded, …).
  useEffect(() => {
    setText(String(value))
    lastValid.current = value
  }, [value])

  /** Parse + clamp + commit; NaN/empty reverts to the last valid value. */
  const commit = (raw: string): void => {
    const parsed = parseInt(raw, 10)
    if (Number.isNaN(parsed)) {
      setText(String(lastValid.current))
      return
    }
    const clamped = Math.min(Math.max(parsed, min), max)
    setText(String(clamped))
    lastValid.current = clamped
    if (clamped !== value) onChange(clamped)
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === 'Enter') {
      e.preventDefault()
      commit(text)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setText(String(lastValid.current))
    } else if (e.key === 'ArrowDown') {
      // ArrowDown opens the preset menu (ARIA combobox convention); Alt is
      // accepted too. The caret jump it would cause in a single-line numeric
      // field is not meaningful.
      e.preventDefault()
      setOpen(true)
    }
  }

  return (
    <div
      className={cn(
        'c0-input flex h-8 w-full min-w-0 items-center gap-1 rounded-md border border-input px-2 text-sm',
        'focus-within:border-primary focus-within:ring-1 focus-within:ring-primary/30',
        'has-disabled:cursor-not-allowed has-disabled:opacity-50',
        className,
      )}
    >
      <input
        type="text"
        inputMode="numeric"
        aria-label={ariaLabel}
        disabled={disabled}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKeyDown}
        onBlur={() => commit(text)}
        className="h-full w-full min-w-0 flex-1 bg-transparent text-foreground outline-none disabled:cursor-not-allowed disabled:opacity-50"
      />
      {unit && (
        <span className="pointer-events-none shrink-0 select-none text-xs text-muted-foreground">
          {unit}
        </span>
      )}
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild disabled={disabled}>
          <button
            type="button"
            aria-label={`${ariaLabel} presets`}
            disabled={disabled}
            title={`${ariaLabel} presets`}
            className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
          >
            <ChevronDownIcon className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent aria-label={`${ariaLabel} presets`} align="end">
          {presets.map((p) => {
            const isSelected = p === value
            return (
              <DropdownMenuItem
                key={p}
                data-selected={isSelected}
                className={cn('gap-2 px-3 py-1.5 text-xs', isSelected && 'bg-primary/10 font-medium')}
                onSelect={() => {
                  setText(String(p))
                  lastValid.current = p
                  if (p !== value) onChange(p)
                }}
              >
                <span className="flex-1 text-left">
                  {p}
                  {unit ? ` ${unit}` : ''}
                </span>
                {isSelected && <CheckIcon className="size-3.5 shrink-0 text-primary" />}
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
