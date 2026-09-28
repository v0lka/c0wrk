import { useEffect, useId, useRef, useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'
import { collapsibleRegistry } from './collapsibleRegistry'
import { useChatHoverChevron } from './chatHoverStore'

interface CollapsibleBlockProps {
  icon?: React.ReactNode
  label: React.ReactNode
  statusIcon?: React.ReactNode
  badge?: React.ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
  className?: string
  children: React.ReactNode
  headerExtra?: React.ReactNode
  /**
   * A stable identifier for the chevron, used by {@link chatHoverStore} to
   * match this collapsible's reveal against the pointer's target. When omitted,
   * a `useId()` is used.
   *
   * Prefer passing a value derived from the underlying item's stable key (e.g.
   * `bookmarkKey(item)`) over falling back to `useId()`: `useId()` is bound to
   * the component's tree position, so a remount or a reorder while the pointer
   * is still over the chevron would change the id and silently hide the chevron
   * until the next `mouseover`. A stable item-derived id survives those.
   */
  revealId?: string
}

export function CollapsibleBlock({
  icon,
  label,
  statusIcon,
  badge,
  open: controlledOpen,
  onOpenChange,
  className,
  children,
  headerExtra,
  revealId,
}: CollapsibleBlockProps) {
  // Uncontrolled fallback — LIVE, not dead code: ToolCard, ThoughtBlock,
  // ThoughtGroupBlock and ReflectionBlock render this component without
  // open/onOpenChange and rely on this state. The block starts COLLAPSED and
  // toggles itself; there is deliberately no `defaultOpen` prop (the controlled
  // callers — PlanStepBlock / SubAgentBlock — own their open state, and the
  // former auto-open on error/running was removed on purpose), so no caller
  // needs a default-open uncontrolled block.
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false)

  const isControlled = controlledOpen !== undefined
  const isOpen = isControlled ? controlledOpen : uncontrolledOpen
  const setOpen = isControlled ? onOpenChange : setUncontrolledOpen

  // The chevron's visibility is driven EXCLUSIVELY, and programmatically, by
  // the {@link chatHoverStore} (a single active chevron across the whole chat).
  // There is deliberately NO CSS `group`/`group-hover`/Tailwind opacity
  // fallback: visibility is an inline style toggled from the store, so nothing
  // is left to the browser's `:hover` cascade (the down-sweep-accumulates /
  // up-sweep-clears symptom) and a hidden chevron is never a live hit-target.
  //
  // `revealId` is preferred (stable item-derived id, resurvives remount/reorder
  // while hovered) over a `useId()` fallback for exactly the same reason the
  // store replaced CSS `:hover` — see the interface doc for {@link revealId}.
  const fallbackId = useId()
  const chevronId = revealId ?? fallbackId
  const activeChevron = useChatHoverChevron()
  const chevronActive = activeChevron === chevronId

  // Register this block's `setOpen` under its chevron id so outside code can
  // programmatically collapse it via {@link collapsibleRegistry}. The
  // registered callback is a stable wrapper that always forwards to the
  // freshest `setOpen` — `setOpen` itself is a different function reference on
  // every render (controlled: an inline `onOpenChange` prop; uncontrolled: a
  // state setter that React re-creates per render), so registering it directly
  // would re-run the effect on every render and, worse, unregister the
  // *current* registration on each cleanup. Keeping the identity stable lets
  // the effect run ONLY when `chevronId` changes: mount registers, id change
  // re-registers (overwriting the old entry), unmount unregisters. Symmetric
  // by construction — no leaks, no stale callbacks.
  const setOpenRef = useRef(setOpen)
  setOpenRef.current = setOpen
  useEffect(() => {
    const registrySetOpen = (open: boolean): void => {
      // `setOpen` is `undefined` only for a controlled caller that omits
      // `onOpenChange` (Radix tolerates that); the registry treats such a
      // block as inert rather than throwing.
      setOpenRef.current?.(open)
    }
    collapsibleRegistry.register(chevronId, registrySetOpen)
    return () => {
      collapsibleRegistry.unregister(chevronId, registrySetOpen)
    }
  }, [chevronId])

  return (
    <Collapsible
      open={isOpen}
      onOpenChange={setOpen}
      data-chevron-reveal-id={chevronId}
      className={cn('group min-w-0', className)}
    >
      {/*
       * Width contract for ellipsis in the header row.
       *
       * CollapsibleTrigger renders a <button>. Unlike a block <div>, a <button>
       * shrink-to-fits to its content and does NOT stretch to fill its
       * container — so a long title (e.g. a bash_exec command) makes the button
       * grow to full content width, spilling past the chat edge and forcing a
       * horizontal scrollbar. With no width pressure on its flex children,
       * `min-w-0 truncate` does nothing and no overflow is ever detected.
       *
       * `max-w-full` caps the button at the chat column width (when content
       * exceeds it, the button = column width; when content is short, the button
       * stays content-width — no layout change for short titles). `min-w-0` lets
       * it shrink as a flex item. Together they force the flex children — the
       * label span carrying `min-w-0 truncate` — to actually ellipsize, which in
       * turn makes `scrollWidth > clientWidth` reliable for overflow-gated
       * tooltips.
       *
       * Because the button is now a width-constrained flex row, every icon-like
       * flex child MUST carry `shrink-0`: for SVGs `min-width:auto` resolves to 0
       * (SVG has overflow:hidden), so without `flex-shrink:0` the glyph shrinks
       * toward 0 as the title overflows. The chevron span, the tool `icon`, and
       * the `statusIcon` all carry it.
       */}
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors max-w-full min-w-0">
        <span
          className="inline-flex shrink-0"
          style={{ visibility: chevronActive ? 'visible' : 'hidden' }}
        >
          {isOpen ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
        </span>
        {statusIcon}
        {icon}
        {typeof label === 'string'
          ? <span className="text-sm min-w-0 truncate">{label}</span>
          : label}
        {badge}
        {headerExtra}
      </CollapsibleTrigger>
      <CollapsibleContent>
        {children}
      </CollapsibleContent>
    </Collapsible>
  )
}
