import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { CheckIcon, ChevronDownIcon } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useSelectedOptionScroll } from '@/components/ui/useSelectedOptionScroll'
import { cn } from '@/lib/utils'

interface StringComboboxProps {
  /** Current string value (a name, an id, …). */
  value: string
  /** Preset choices offered in the dropdown. Arbitrary values not in the
   *  list are equally valid — the text field is the primary input. */
  options: readonly string[]
  onChange: (v: string) => void
  /** Optional inline style for the text input, e.g. rendering the current
   *  value in its own typeface (the font pickers pass composeFontFamily(…)
   *  from lib/fonts). Typography only — the control's look stays
   *  className-driven. */
  inputStyle?: CSSProperties
  /** Optional per-option inline style for the dropdown labels, e.g. render
   *  every font name in its own typeface. Receives the option string. */
  itemStyle?: (option: string) => CSSProperties
  /**
   * Optional normalization applied to typed text on commit (Enter / blur),
   * e.g. trim or slugify. An empty input — or one that normalizes to the
   * empty string — reverts to the last valid value. Preset picks bypass
   * normalize: they are already valid by construction (mirroring how
   * `EditableCombobox` trusts its presets and clamps only typed input).
   */
  normalize?: (raw: string) => string
  /** Accessible name for the text input and the chevron button. */
  ariaLabel: string
  /** When false the field is select-only: the input is read-only and the
   *  dropdown is the sole input — for enum-valued settings where free text
   *  is meaningless (font smoothing, …). Default true (free text). */
  editable?: boolean
  disabled?: boolean
  className?: string
}

// Long option lists need a scrollable menu for the selected-option
// scrollIntoView to have something to scroll within (same sizing as `Combobox`).
const MIN_WIDTH_CLASS = 'min-w-72'
const MAX_HEIGHT_CLASS = 'max-h-64'

/**
 * StringCombobox — a free-text input with a preset dropdown for settings
 * forms: the string-valued sibling of `EditableCombobox` (same Radix
 * `dropdown-menu` primitives, so it works inside modal Radix dialogs for the
 * same reasons as `Combobox`).
 *
 * The text field is the primary input and accepts ARBITRARY values — the
 * dropdown only offers presets — unless `editable` is false: then the field
 * is select-only (read-only input, the dropdown is the sole input) for
 * enum-valued settings. Local text state mirrors `value` until the user
 * types; commits happen on Enter, blur or preset pick. A commit runs the
 * optional `normalize` and is rejected when the result is empty, restoring
 * the last valid value (no onChange). Escape reverts the pending edit without
 * committing.
 *
 * Menu selection feedback follows `Combobox`: the option equal to the current
 * value carries `data-selected="true"`, a check icon, and is scrolled into
 * view when the menu opens (an arbitrary value matching no option simply
 * leaves every option unchecked).
 */
export function StringCombobox({
  value,
  options,
  onChange,
  normalize,
  ariaLabel,
  editable = true,
  disabled = false,
  className,
  inputStyle,
  itemStyle,
}: StringComboboxProps) {
  const [text, setText] = useState(value)
  const [open, setOpen] = useState(false)
  const lastValid = useRef(value)

  // Keep the field in sync when the controlled value changes externally
  // (preset picked elsewhere, settings reloaded, …).
  useEffect(() => {
    setText(value)
    lastValid.current = value
  }, [value])

  // `disabled` reaches the TRIGGER only (same gap as in `Combobox`): a menu
  // opened before the flip stays mounted in its portal with clickable items.
  // Force it shut; the functional update is a no-op while usable.
  useEffect(() => {
    setOpen((prev) => (disabled ? false : prev))
  }, [disabled])

  /** Normalize + commit; empty result reverts to the last valid value. */
  const commit = (raw: string): void => {
    const next = normalize ? normalize(raw) : raw
    if (!next) {
      setText(lastValid.current)
      return
    }
    setText(next)
    lastValid.current = next
    if (next !== value) onChange(next)
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === 'Enter') {
      e.preventDefault()
      commit(text)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setText(lastValid.current)
    } else if (e.key === 'ArrowDown') {
      // ArrowDown opens the options menu (ARIA combobox convention); Alt is
      // accepted too. Same tradeoff as `EditableCombobox`: the caret jump it
      // would cause in a single-line text field is not meaningful.
      e.preventDefault()
      setOpen(true)
    }
  }

  // Selected-option scrollIntoView on menu open — once per open cycle; the
  // hook's doc comment explains why the Radix-composed ref must not scroll
  // on every re-attach (it made the open list snap back to the selected
  // option while the user scrolled it).
  const handleContentRef = useSelectedOptionScroll(open)

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
        aria-label={ariaLabel}
        disabled={disabled}
        readOnly={!editable}
        value={text}
        onChange={(e) => {
          // Select-only fields ignore programmatic input events too — a real
          // browser never fires `input` on a readOnly field, but autofill or
          // synthetic events could; the dropdown remains the sole input.
          if (editable) setText(e.target.value)
        }}
        onKeyDown={handleKeyDown}
        onBlur={() => commit(text)}
        style={inputStyle}
        className="h-full w-full min-w-0 flex-1 bg-transparent text-foreground outline-none disabled:cursor-not-allowed disabled:opacity-50"
      />
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild disabled={disabled}>
          <button
            type="button"
            aria-label={`${ariaLabel} options`}
            disabled={disabled}
            title={`${ariaLabel} options`}
            className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
          >
            <ChevronDownIcon className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          ref={handleContentRef}
          aria-label={`${ariaLabel} options`}
          align="end"
          className={cn(MIN_WIDTH_CLASS, MAX_HEIGHT_CLASS)}
        >
          {options.map((opt) => {
            const isSelected = opt === value
            return (
              <DropdownMenuItem
                key={opt}
                data-selected={isSelected}
                className={cn('gap-2 px-3 py-1.5 text-xs', isSelected && 'bg-primary/10 font-medium')}
                // Re-picking the already-selected value is a no-op (native
                // <select> fires no `change` event either); this avoids
                // spurious config-save round-trips, as in `Combobox`.
                onSelect={() => {
                  setText(opt)
                  lastValid.current = opt
                  if (opt !== value) onChange(opt)
                }}
              >
                <span className="flex-1 text-left truncate" style={itemStyle?.(opt)}>{opt}</span>
                {isSelected && <CheckIcon className="size-3.5 shrink-0 text-primary" />}
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
