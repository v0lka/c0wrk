import { useLayoutEffect, useRef, useState } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

interface BudgetPreset {
  /** Display label shown in the menu and on the trigger. */
  label: string
  /**
   * Budget value stored in inputModeStore and sent to the backend. A JSON
   * object of the goal.GoalBudget fields; empty string = unlimited. The
   * backend parses and applies these as caps.
   */
  value: string
}

/**
 * Goal budget presets, expressed as turn caps. The value is the
 * goal.GoalBudget JSON override sent to the backend (empty = unlimited).
 * The budget is turn-only, so each preset sets the single `max_turns`
 * dimension.
 *
 * Beyond these presets, a custom turn count may be typed via the menu's custom
 * input row, which produces `{"max_turns":N}` for any positive integer N.
 */
const BUDGET_PRESETS: BudgetPreset[] = [
  { label: '∞', value: '' },
  { label: '3 turns', value: '{"max_turns":3}' },
  { label: '5 turns', value: '{"max_turns":5}' },
  { label: '10 turns', value: '{"max_turns":10}' },
]

/** Resolve the display label for a stored budget value. */
function labelFor(value: string): string {
  const match = BUDGET_PRESETS.find((p) => p.value === value)
  if (match) return match.label
  // Non-preset value: parse max_turns from the JSON and surface it. Mirrors
  // the preset labels so the trigger reads e.g. "7 turns".
  const parsed = parseMaxTurns(value)
  return parsed === null ? '∞' : `${parsed} turns`
}

/**
 * Extract `max_turns` from a goal.GoalBudget JSON string. Returns null when
 * the value is empty, not JSON, or lacks a numeric max_turns — callers treat
 * null as "unlimited / unknown".
 */
function parseMaxTurns(value: string): number | null {
  if (!value) return null
  try {
    const obj = JSON.parse(value) as unknown
    if (typeof obj !== 'object' || obj === null) return null
    const raw = (obj as Record<string, unknown>).max_turns
    if (typeof raw !== 'number' || !Number.isFinite(raw)) return null
    return raw
  } catch {
    return null
  }
}

/**
 * BudgetCombobox selects a goal budget override for the next sent message. It
 * offers a small set of turn-cap presets (∞ unlimited default, 3/5/10 turns)
 * plus a custom turn-count input row at the bottom of the menu. The selection
 * is persisted in inputModeStore and survives restarts.
 *
 * Only meaningful when goal mode is enabled — the parent hides it otherwise.
 *
 * The menu is built on the project's Radix `popover` primitives (Popover, not
 * Menu — the panel embeds a custom input row). Radix portals the content to
 * <body> and positions it with the popper (collision-aware, zoom-patched
 * globally), so the panel escapes the message input area's overflow-hidden
 * ancestor and is never clipped when the input panel is at its minimum height.
 */
export function BudgetCombobox({ disabled = false }: { disabled?: boolean }) {
  const goalBudget = useInputModeStore((s) => s.goalBudget)
  const setGoalBudget = useInputModeStore((s) => s.setGoalBudget)
  const [isOpen, setIsOpen] = useState(false)
  const customInputRef = useRef<HTMLInputElement>(null)
  const [customValue, setCustomValue] = useState<string>('')

  const displayLabel = labelFor(goalBudget)

  // Seed the custom input with the current max_turns whenever the menu opens,
  // so a non-preset value is visible and editable without retyping.
  useLayoutEffect(() => {
    if (isOpen) {
      const current = parseMaxTurns(goalBudget)
      setCustomValue(current !== null && current > 0 ? String(current) : '')
    }
  }, [isOpen, goalBudget])

  const close = () => setIsOpen(false)

  // Apply a typed turn count. Guards against NaN and <= 0 (ignored). Commits
  // the goal.GoalBudget JSON and closes the menu, mirroring preset selection.
  const applyCustom = () => {
    const n = Number(customValue)
    if (!Number.isFinite(n) || n <= 0) return
    setGoalBudget(JSON.stringify({ max_turns: n }))
    close()
  }

  return (
    <div className="relative shrink-0">
      <Popover open={isOpen} onOpenChange={setIsOpen}>
        <PopoverTrigger asChild disabled={disabled}>
          <button
            type="button"
            disabled={disabled}
            className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[140px] truncate disabled:opacity-50 disabled:cursor-not-allowed"
            title={disabled ? 'Locked while the session is running' : `Budget: ${displayLabel}`}
            aria-label="Select goal budget"
          >
            <span className="truncate">{displayLabel}</span>
            <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M6 9l6 6 6-6" />
            </svg>
          </button>
        </PopoverTrigger>

        <PopoverContent
          role="listbox"
          aria-label="Select goal budget"
          align="start"
          sideOffset={6}
          collisionPadding={8}
          className="w-40 p-0"
          // The chat input's own Escape handling must not also fire while the
          // menu consumes the keystroke to close itself (Radix still closes).
          onEscapeKeyDown={(e) => e.stopPropagation()}
          // Radix moves focus to the first focusable node in the content (the
          // first preset button) by default; the custom turn-count input is the
          // intended landing spot so typing starts immediately.
          onOpenAutoFocus={(e) => {
            e.preventDefault()
            customInputRef.current?.focus()
          }}
        >
          {BUDGET_PRESETS.map((preset) => {
            const isSelected = goalBudget === preset.value
            return (
              <button
                key={preset.label}
                type="button"
                className={cn(
                  'flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted',
                  isSelected && 'bg-primary/10 font-medium',
                )}
                onClick={() => { setGoalBudget(preset.value); close() }}
                title={`Set the goal budget to ${preset.label}`}
              >
                <span className="flex-1 text-left">{preset.label}</span>
                {isSelected && (
                  <span className="text-xs text-primary">selected</span>
                )}
              </button>
            )
          })}

          {/* Custom turn-count input. Lives at the bottom of the menu;
              confirming a positive integer sets {"max_turns":N}. */}
          <div className="border-t border-border px-3 py-1.5">
            <div className="flex items-center gap-1.5">
              <input
                ref={customInputRef}
                type="number"
                min={1}
                inputMode="numeric"
                placeholder="custom"
                aria-label="Custom turn count"
                value={customValue}
                onChange={(e) => setCustomValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    applyCustom()
                  }
                }}
                className="w-16 px-1.5 py-0.5 text-xs rounded border border-input bg-background text-foreground focus:outline-none focus:ring-1 focus:ring-primary"
              />
              <span className="text-xs text-muted-foreground">turns</span>
              <button
                type="button"
                className="ml-auto px-1.5 py-0.5 text-xs rounded border border-input bg-background hover:bg-muted text-foreground transition-colors"
                onClick={applyCustom}
                title="Apply the custom turn count"
              >
                Apply
              </button>
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}
