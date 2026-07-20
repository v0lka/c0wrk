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

// Portal positioning constants — mirror ModelCombobox so the menu opens upward
// in the toolbar and is never clipped by the input's overflow-hidden ancestor.
const MAX_DROPDOWN_HEIGHT = 256
const MIN_WIDTH = 160
const GAP = 6
const Z_INDEX = 50

/**
 * BudgetCombobox selects a goal budget override for the next sent message. It
 * offers a small set of turn-cap presets (∞ unlimited default, 3/5/10 turns)
 * plus a custom turn-count input row at the bottom of the menu. The selection
 * is persisted in inputModeStore and survives restarts.
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
  const customInputRef = useRef<HTMLInputElement>(null)
  const [position, setPosition] = useState<DropdownPosition | null>(null)
  const [customValue, setCustomValue] = useState<string>('')

  const displayLabel = labelFor(goalBudget)

  // Seed the custom input with the current max_turns whenever the menu opens,
  // so a non-preset value is visible and editable without retyping.
  useLayoutEffect(() => {
    if (isOpen) {
      const current = parseMaxTurns(goalBudget)
      setCustomValue(current !== null && current > 0 ? String(current) : '')
      // Focus the input on next tick so the menu has measured its position.
      requestAnimationFrame(() => customInputRef.current?.focus())
    }
  }, [isOpen, goalBudget])

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

  // Apply a typed turn count. Guards against NaN and <= 0 (ignored). Commits
  // the goal.GoalBudget JSON and closes the menu, mirroring preset selection.
  const applyCustom = () => {
    const n = Number(customValue)
    if (!Number.isFinite(n) || n <= 0) return
    setGoalBudget(JSON.stringify({ max_turns: n }))
    close()
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

          {/* Custom turn-count input. Lives at the bottom of the portaled
              menu; confirming a positive integer sets {"max_turns":N}. */}
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
              >
                Apply
              </button>
            </div>
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
