import { Fragment, useCallback, useState, type ReactNode } from 'react'
import { CheckIcon, ChevronDownIcon } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export interface ComboboxOption {
  value: string
  label: string
  /**
   * Marks the option as unselectable (rendered dimmed, pointer-events none —
   * Radix never fires onSelect for a disabled item). Used e.g. for silent-mode
   * judge-dependent modes while no LLM model is configured for the judge.
   */
  disabled?: boolean
  /**
   * Optional hover tooltip content, rendered inside a portaled Radix tooltip
   * anchored to the menu item — the generic settings combobox has no other
   * description surface, and Radix tooltips are zoom-corrected globally
   * (lib/floatingUiZoom). The caller supplies the node (e.g. rendered
   * markdown), so this primitive stays free of any rendering dependency.
   */
  tooltip?: ReactNode
}

/**
 * Menu sizing (mirrors the previous hand-rolled portaled menu so the visual
 * footprint is unchanged): MIN_WIDTH keeps long option labels readable
 * (`min-w-72` = 288px), MAX_HEIGHT caps the list so long model lists scroll.
 */
const MIN_WIDTH_CLASS = 'min-w-72'
const MAX_HEIGHT_CLASS = 'max-h-64'

interface ComboboxProps {
  /** Currently selected option value. */
  value: string
  options: readonly ComboboxOption[]
  onChange: (value: string) => void
  /** Accessible name for the trigger button and the portaled menu. */
  ariaLabel: string
  /** Trigger label shown when `value` matches no option. */
  placeholder?: string
  disabled?: boolean
  /**
   * Trigger overrides (height, width, font size). Base styling comes from the
   * chat toolbar comboboxes; conflicts resolve via tailwind-merge, so e.g.
   * `h-8 w-auto text-xs` narrows the default `h-9 w-full text-sm` form.
   */
  className?: string
}

/**
 * Combobox — a single-select dropdown for settings forms, built on the
 * project's Radix `dropdown-menu` primitives.
 *
 * Native `<select>` elements are avoided in settings because their popup
 * option lists render with the OS palette on Windows (light background / dark
 * text regardless of the app theme) and cannot be themed from CSS. This
 * component renders the menu itself with the project's design tokens
 * (`bg-popover`, `focus:bg-muted/50`, `bg-primary/10` selection) so colors
 * stay correct on every platform.
 *
 * Why Radix instead of a hand-rolled portal (like the chat toolbar
 * ModelCombobox): the settings dropdowns live inside modal Radix dialogs,
 * which set `body { pointer-events: none }`. A plain portal to document.body
 * inherits `none` — the menu renders but is inert, and clicks on options fall
 * through and close the dialog. Radix's `DropdownMenu.Content` is a
 * `DismissableLayer`: while a parent modal layer owns the body lock, the
 * content gets inline `pointer-events: auto`, clicks inside it count as
 * "inside" the dialog (no accidental dismissal), and Escape closes only the
 * menu, not the dialog. Keyboard support (arrows/Home/End/type-ahead) and
 * popper positioning (flip on collision, resize/scroll/content-size tracking)
 * also come from the primitives.
 */
export function Combobox({
  value,
  options,
  onChange,
  ariaLabel,
  placeholder,
  disabled = false,
  className,
}: ComboboxProps) {
  const [open, setOpen] = useState(false)

  const selected = options.find((o) => o.value === value) ?? null
  const displayLabel = selected?.label ?? placeholder ?? value

  // Long option lists (e.g. every enabled model) can push the selected option
  // below the `max-h-64` fold. Radix focuses the first item on keyboard open,
  // so without this the selected one would only be visible by scrolling.
  // A stable ref callback (not a state/effect pair) is required for two
  // reasons: Radix mounts the portaled content one commit AFTER `open`
  // flips — so an effect keyed on `open` runs too early — and a new function
  // identity per render would make React detach/reattach the ref on every
  // re-render, re-scrolling on unrelated updates. useCallback keeps it
  // attach-once-per-mount. jsdom has no scrollIntoView; guard the call.
  const handleContentRef = useCallback((node: HTMLDivElement | null) => {
    if (!node) return
    const selectedEl = node.querySelector<HTMLElement>('[data-selected="true"]')
    if (selectedEl && typeof selectedEl.scrollIntoView === 'function') {
      selectedEl.scrollIntoView({ block: 'nearest' })
    }
  }, [])

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button
          type="button"
          disabled={disabled}
          aria-label={ariaLabel}
          title={displayLabel}
          className={cn(
            'flex h-9 w-full items-center gap-1 rounded-md border border-input bg-background px-3 text-sm',
            'text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors truncate',
            'disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-none',
            className,
          )}
        >
          <span className="truncate">{displayLabel}</span>
          <ChevronDownIcon className="size-4 shrink-0" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        ref={handleContentRef}
        aria-label={ariaLabel}
        align="start"
        className={cn(MIN_WIDTH_CLASS, MAX_HEIGHT_CLASS)}
      >
        {options.length === 0 && (
          <div className="px-3 py-2 text-xs text-muted-foreground italic">No options available.</div>
        )}
        {options.map((opt) => {
          const isSelected = opt.value === value
          const item = (
            <DropdownMenuItem
              data-selected={isSelected}
              disabled={opt.disabled}
              className={cn('gap-2 px-3 py-1.5 text-xs', isSelected && 'bg-primary/10 font-medium')}
              // Re-picking the already-selected value is a no-op (native
              // <select> fires no `change` event either); this avoids spurious
              // config-save round-trips in SecurityGroupCard/SearchSettings.
              // A disabled option is additionally guarded (Radix suppresses
              // its select events, this keeps the contract local).
              onSelect={() => {
                if (!opt.disabled && opt.value !== value) onChange(opt.value)
              }}
            >
              <span className="flex-1 text-left truncate">{opt.label}</span>
              {isSelected && <CheckIcon className="size-3.5 shrink-0 text-primary" />}
            </DropdownMenuItem>
          )
          // Without a tooltip the item renders bare (unchanged behavior). With
          // one, a portaled Radix tooltip wraps it — the trigger *is* the menu
          // item, so hovering anywhere on the row reveals the description.
          if (!opt.tooltip) {
            return <Fragment key={opt.value}>{item}</Fragment>
          }
          return (
            <Tooltip key={opt.value}>
              <TooltipTrigger asChild>{item}</TooltipTrigger>
              <TooltipContent
                side="right"
                align="start"
                sideOffset={6}
                collisionPadding={16}
                // Full registry descriptions (the Model Profiles picker) can run to
                // several hundred characters; without a cap the body-portaled
                // tooltip is simply clipped by the window and the tail becomes
                // unreadable. Cap and scroll it like every other long-content
                // tooltip (StepTooltip/StepResultTooltip); the cap uses the
                // zoom-corrected `--ui-vh` primitive (see lib/layoutSpace.ts).
                className="max-w-sm max-h-[min(calc(var(--ui-vh)*0.7),calc(var(--radix-tooltip-content-available-height)-16px))] overflow-y-auto custom-scrollbar text-left text-wrap"
              >
                {opt.tooltip}
              </TooltipContent>
            </Tooltip>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
