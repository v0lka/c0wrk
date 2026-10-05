import { forwardRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * SegmentedControl — the app-wide switcher for mutually exclusive modes/views.
 *
 * ONE chrome for every segmented surface: a borderless track with a
 * high-contrast active pill (`bg-primary text-primary-foreground` — light-gray
 * pill on dark background in the dark theme, blue pill in the light theme),
 * compact `text-xs` items by default. Replaces the ad-hoc per-panel switchers
 * (bordered button pairs, underlined tab strips, ghost-button rows, Radix
 * TabsLists).
 *
 * Two ARIA flavors via `semantic`:
 * - `"tabs"` (default) — the ARIA tabs pattern (`role=tablist`/`tab` +
 *   `aria-selected`) for switches that change the visible VIEW (panel tabs).
 *   Keyboard: roving tabindex + ArrowLeft/ArrowRight/Home/End.
 * - `"radio"` — `role=radiogroup`/`radio` + `aria-checked` for switches that
 *   pick an OPTION rather than a view (settings modes, verification mode).
 *   Same arrow-key model.
 *
 * Sizing: `sm` (compact, text-xs — panel tabs, toolbars, settings cards) and
 * `md` (text-sm font-medium — larger panel/settings switchers). Items
 * carry FIXED heights (`h-5`/`h-7`) so a segment's height never depends on
 * its content: responsively hidden labels (`labelClassName="hidden …:inline"`,
 * e.g. the sidebar workspace/git switchers) collapse the button to its icon
 * without the 28→24px height jump a content-derived height would cause.
 */
export type SegmentedSemantic = 'tabs' | 'radio'
export type SegmentedSize = 'sm' | 'md'

export interface SegmentedControlItem<V extends string | number> {
  value: V
  /** Text label. Omit for icon-only items. textContent stays verbatim (tests
   *  assert e.g. 'files'); visual casing is the caller's concern. A STRING
   *  label is also the item's default `title` (see `title` below) — segments
   *  may hide their label responsively (`labelClassName="hidden …:inline"`),
   *  and hide-capable markup must keep a tooltip. */
  label?: ReactNode
  /** Leading icon node (already sized, e.g. `<GitBranch className="size-3.5" />`). */
  icon?: ReactNode
  /** Native tooltip / accessible name for icon-only items. When omitted, a
   *  string `label` is used as the title automatically (an echo is the
   *  correct tooltip for label-hiding segments); a non-string label and an
   *  icon-only item without a title render no title. */
  title?: string
  /** Stable test hook (rendered as data-testid on the item button). */
  testId?: string
  /** Extra props spread onto this item's `<button>` (e.g. `id`,
   *  `aria-controls`). Exists so a panel-owning `tabs` caller can complete the
   *  ARIA tabs association — the tab carries `id` + `aria-controls`, its
   *  `role="tabpanel"` mirrors them with `id` + `aria-labelledby` (the
   *  Research panel's Dashboard/Papers control). The control's own props
   *  (`type`, role/aria-selected, `title`, `disabled`, the `data-*` hooks,
   *  `tabIndex`, `onClick`) are applied AFTER the spread, so an `itemProps`
   *  entry can never break the keyboard model or the selection state. */
  itemProps?: React.ComponentPropsWithoutRef<'button'>
}

export interface SegmentedControlProps<V extends string | number> {
  items: ReadonlyArray<SegmentedControlItem<V>>
  /** The selected value, or `null` for "nothing selected yet". `null` keeps
   *  the group keyboard-reachable: the FIRST item carries `tabIndex=0` (the
   *  roving tabindex anchor) and an arrow key from it selects as usual.
   *  A value that matches NO item ("orphaned", e.g. a stale persisted id) is
   *  also keyboard-safe: the first item is anchored the same way, and the
   *  first arrow key press selects a real value. */
  value: V | null
  onValueChange: (value: V) => void
  semantic?: SegmentedSemantic
  size?: SegmentedSize
  /** Stretch the track and its items to the full row width. */
  fullWidth?: boolean
  /** Accessible name for the group (required). */
  ariaLabel: string
  /** Disable every item (e.g. while a blocking operation is in flight). */
  disabled?: boolean
  className?: string
  /** Extra classes for every item button (e.g. 'capitalize'). */
  itemClassName?: string
  /** Extra classes for every item's label span. The label is ALWAYS wrapped in
   *  a span (even when it looks like bare text) so callers can hide it
   *  reactively — e.g. `hidden @min-[260px]:inline` to drop text labels on a
   *  narrow sidebar while the icons stay. Pair with an `@container` ancestor. */
  labelClassName?: string
  'data-testid'?: string
}

const TRACK_CLASS = 'inline-flex items-center gap-0.5 rounded-md p-0.5'

const SIZE_ITEM_CLASS: Record<SegmentedSize, string> = {
  sm: 'h-5 px-2 py-0.5 text-xs gap-1',
  md: 'h-7 px-2 py-1 text-sm font-medium gap-1',
}

const SELECTED_CLASS = 'bg-primary text-primary-foreground font-medium'
const UNSELECTED_CLASS = 'text-muted-foreground hover:text-foreground'

/** Inner implementation — generic across the string union of item values. */
function SegmentedControlInner<V extends string | number>(
  {
    items,
    value,
    onValueChange,
    semantic = 'tabs',
    size = 'sm',
    fullWidth = false,
    ariaLabel,
    disabled = false,
    className,
    itemClassName,
    labelClassName,
    'data-testid': testId,
  }: SegmentedControlProps<V>,
  ref: React.Ref<HTMLDivElement>,
) {
  const isTabs = semantic === 'tabs'
  const values = items.map((item) => item.value)

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    // Empty track: no segments to move between. Without this guard the modulo
    // below divides by zero and `nextIndex` becomes NaN (a silent no-op that
    // has already swallowed preventDefault).
    if (values.length === 0) return
    const currentIndex = values.indexOf(value ?? values[0]!)
    let nextIndex: number
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        nextIndex = (currentIndex + 1) % values.length
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        nextIndex = (currentIndex - 1 + values.length) % values.length
        break
      case 'Home':
        nextIndex = 0
        break
      case 'End':
        nextIndex = values.length - 1
        break
      default:
        return
    }
    e.preventDefault()
    const next = values[nextIndex]
    if (next === undefined) return
    onValueChange(next)
    // Move focus with the roving tabindex so the newly selected item receives
    // it. Synchronous, NOT inside requestAnimationFrame: React may commit the
    // re-render (which flips tabIndex to 0 on the new segment) after the rAF
    // callback fires, and re-focusing a node that still carries tabIndex=-1
    // post-commit gets the focus stripped, ejecting the keyboard user from the
    // control entirely. Focusing synchronously from the handler is stable —
    // the DOM node persists across the commit (stable segment keys), and a
    // programmatic focus on a tabIndex=-1 element is legal even in the
    // uncontrolled-value corner case where the commit never flips it.
    e.currentTarget
      .querySelector<HTMLElement>(`[data-segment-value="${next}"]`)
      ?.focus()
  }

  return (
    <div
      ref={ref}
      role={isTabs ? 'tablist' : 'radiogroup'}
      aria-label={ariaLabel}
      aria-orientation="horizontal"
      data-testid={testId}
      onKeyDown={onKeyDown}
      className={cn(TRACK_CLASS, fullWidth && 'flex w-full', className)}
    >
      {items.map((item) => {
        const selected = value === item.value
        const itemClass = cn(
          'inline-flex items-center justify-center rounded transition-colors whitespace-nowrap',
          SIZE_ITEM_CLASS[size],
          selected ? SELECTED_CLASS : UNSELECTED_CLASS,
          itemClassName,
        )
        return (
          <button
            key={item.value}
            type="button"
            {...item.itemProps}
            {...(isTabs
              ? { role: 'tab', 'aria-selected': selected }
              : { role: 'radio', 'aria-checked': selected })}
            title={item.title ?? (typeof item.label === 'string' ? item.label : undefined)}
            disabled={disabled}
            data-segment-value={item.value}
            data-active={selected}
            data-testid={item.testId}
            tabIndex={
              selected ||
              ((value === null || !values.includes(value)) && item.value === values[0])
                ? 0
                : -1
            }
            onClick={() => onValueChange(item.value)}
            className={itemClass}
          >
            {item.icon}
            {item.label !== undefined && (
              <span className={cn(labelClassName)}>{item.label}</span>
            )}
          </button>
        )
      })}
    </div>
  )
}

/**
 * Generic forwardRef wrapper: `<SegmentedControl<V> items={...} …/>` in JSX
 * (the explicit type argument is what makes `value`/`onValueChange` typecheck
 * against the caller's union). forwardRef keeps the component usable as a
 * Radix `asChild` child if a call site ever needs that.
 */
export const SegmentedControl = forwardRef(SegmentedControlInner) as <V extends string | number>(
  props: SegmentedControlProps<V> & { ref?: React.Ref<HTMLDivElement> },
) => React.ReactElement
