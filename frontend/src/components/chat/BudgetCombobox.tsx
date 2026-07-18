import { useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useDropdown } from '@/hooks/useDropdown'
import { computeDropdownPosition, type DropdownPosition } from '@/lib/dropdownPosition'
import { cn } from '@/lib/utils'

interface BudgetPreset {
  /** Display label shown in the menu and on the trigger. */
  label: string
  /**
   * Budget value stored in inputModeStore and sent to the backend. A JSON
   * object of the goal.GoalBudget fields; empty string = use config defaults
   * (unlimited). The backend parses and applies these as caps.
   */
  value: string
}

/**
 * Goal budget presets. The value is the goal.GoalBudget JSON override sent to
 * the backend (empty = config defaults / unlimited). `max_turns:0` means
 * unlimited; the backend merges the override field-by-field over the config
 * defaults, so a preset only tightens the dimension it sets.
 *
 * `token cap` sets a moderate max_tokens cap to bound spending on costly
 * models (config defaults are generous: input ~1M, output ~200K).
 */
const BUDGET_PRESETS: BudgetPreset[] = [
  { label: '∞', value: '' },
  { label: '5 turns', value: '{"max_turns":5}' },
  { label: '10 turns', value: '{"max_turns":10}' },
  { label: 'token cap', value: '{"max_tokens":50000}' },
]

/** Resolve the display label for a stored budget value. */
function labelFor(value: string): string {
  const match = BUDGET_PRESETS.find((p) => p.value === value)
  return match ? match.label : 'Custom'
}

// Portal positioning constants — mirror ModelCombobox so the menu opens upward
// in the toolbar and is never clipped by the input's overflow-hidden ancestor.
const MAX_DROPDOWN_HEIGHT = 256
const MIN_WIDTH = 160
const GAP = 6
const Z_INDEX = 50

/**
 * BudgetCombobox selects a goal budget override for the next sent message. It
 * offers a small set of presets (∞ unlimited default, 5/10 turns, token cap).
 * The selection is persisted in inputModeStore and survives restarts.
 *
 * Only meaningful when goal mode is enabled — the parent hides it otherwise.
 *
 * The menu is rendered through a React portal to `document.body` with
 * `position: fixed` so it is never clipped by the message input area's
 * overflow-hidden ancestor (mirrors ModelCombobox).
 */
export function BudgetCombobox() {
  const goalBudget = useInputModeStore((s) => s.goalBudget)
  const setGoalBudget = useInputModeStore((s) => s.setGoalBudget)
  const { isOpen, setIsOpen, containerRef, menuRef } = useDropdown()

  const triggerRef = useRef<HTMLButtonElement>(null)
  const [position, setPosition] = useState<DropdownPosition | null>(null)

  const displayLabel = labelFor(goalBudget)

  // Position the portaled menu whenever it is open, recomputing on resize and
  // any scroll (capture phase so nested scroll containers are covered too).
  useLayoutEffect(() => {
    if (!isOpen) {
      setPosition(null)
      return
    }

    const recompute = () => {
      const trigger = triggerRef.current
      if (!trigger) return
      const rect = trigger.getBoundingClientRect()
      const menu = menuRef.current
      const dropdownHeight = menu && menu.offsetHeight > 0 ? menu.offsetHeight : MAX_DROPDOWN_HEIGHT
      setPosition(
        computeDropdownPosition({
          triggerRect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width },
          dropdownHeight,
          viewportHeight: window.innerHeight,
          viewportWidth: window.innerWidth,
          gap: GAP,
          minWidth: MIN_WIDTH,
        }),
      )
    }

    recompute()
    window.addEventListener('resize', recompute)
    window.addEventListener('scroll', recompute, true)
    return () => {
      window.removeEventListener('resize', recompute)
      window.removeEventListener('scroll', recompute, true)
    }
  }, [isOpen, menuRef])

  const close = () => {
    setIsOpen(false)
    triggerRef.current?.focus()
  }

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        ref={triggerRef}
        type="button"
        className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[140px] truncate"
        onClick={() => setIsOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === 'Escape' && isOpen) { e.stopPropagation(); close() } }}
        title={`Budget: ${displayLabel}`}
        aria-label="Select goal budget"
      >
        <span className="truncate">{displayLabel}</span>
        <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && createPortal(
        <div
          ref={menuRef}
          role="listbox"
          aria-label="Select goal budget"
          className="rounded-md border bg-popover shadow-md max-h-64 overflow-y-auto custom-scrollbar"
          style={{
            position: 'fixed',
            top: position?.top ?? 0,
            left: position?.left ?? 0,
            width: position?.width ?? MIN_WIDTH,
            visibility: position ? 'visible' : 'hidden',
            zIndex: Z_INDEX,
          }}
          onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }}
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
                onClick={() => { setGoalBudget(preset.value); setIsOpen(false) }}
              >
                <span className="flex-1 text-left">{preset.label}</span>
                {isSelected && (
                  <span className="text-[10px] text-primary">selected</span>
                )}
              </button>
            )
          })}
        </div>,
        document.body,
      )}
    </div>
  )
}
