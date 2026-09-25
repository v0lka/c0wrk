import { ChevronFirst, ChevronUp, FoldVertical, ChevronDown, ChevronLast } from 'lucide-react'
import type { OversizedBlockNavApi } from './useOversizedBlockNav'

interface ChatBlockOverflowToolbarProps {
  /** Navigation API from `useOversizedBlockNav` — the toolbar is pure
   *  presentation over it: it never reads the viewport itself. */
  nav: OversizedBlockNavApi
}

// Design tokens only (no raw lengths beyond the token scale, no raw colors):
// the buttons sit on the chat transcript's floating bottom stack, so they
// mirror the pill language of the "New activity" banner — a rounded-full
// container on `--background` with ghost icon buttons inside. `disabled:`
// variants carry the end-of-list / nothing-to-collapse semantics; the browser
// drops disabled buttons from the tab order and from pointer activation.
const CONTAINER_CLASSES =
  'pointer-events-auto flex items-center gap-0.5 rounded-full border border-border bg-background/90 px-1 py-0.5 shadow-lg'

const BUTTON_CLASSES =
  'inline-flex h-6 w-6 items-center justify-center rounded-full text-muted-foreground transition-colors ' +
  'hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring ' +
  'disabled:pointer-events-none disabled:opacity-40'

/** Native `<button>`s are keyboard-operable by construction (focusable, and
 *  Enter/Space activate them without any component key handling); keeping the
 *  tag native — not a shadcn `Button` — is what guarantees that. */
const BUTTON_TYPE = 'button' as const

/**
 * The block-overflow toolbar: five native icon buttons navigating the chat
 * transcript's expandable (collapsible) blocks — first / previous / collapse
 * the current oversized one / next / last.
 *
 * The component is a pure view over `OversizedBlockNavApi`: disabled states
 * come from `hasPrev`/`hasNext`/`activeRevealId`, clicks delegate to the
 * hook's callbacks. It renders inside ChatScrollManager's single sticky
 * bottom stack alongside the "New activity" banner; the stack wrapper carries
 * `pointer-events-none`, so the toolbar re-enables interaction with
 * `pointer-events-auto` (see CONTAINER_CLASSES).
 */
export function ChatBlockOverflowToolbar({ nav }: ChatBlockOverflowToolbarProps) {
  return (
    <div role="group" aria-label="Expandable block navigation" className={CONTAINER_CLASSES}>
      <button
        type={BUTTON_TYPE}
        onClick={nav.goFirst}
        disabled={!nav.hasPrev}
        title="Jump to first expandable block"
        aria-label="Jump to first expandable block"
        className={BUTTON_CLASSES}
      >
        <ChevronFirst className="size-3" />
      </button>
      <button
        type={BUTTON_TYPE}
        onClick={nav.goPrev}
        disabled={!nav.hasPrev}
        title="Previous expandable block"
        aria-label="Previous expandable block"
        className={BUTTON_CLASSES}
      >
        <ChevronUp className="size-3" />
      </button>
      <button
        type={BUTTON_TYPE}
        onClick={nav.collapse}
        disabled={nav.activeRevealId === null}
        title="Collapse current oversized block"
        aria-label="Collapse current oversized block"
        className={BUTTON_CLASSES}
      >
        <FoldVertical className="size-3" />
      </button>
      <button
        type={BUTTON_TYPE}
        onClick={nav.goNext}
        disabled={!nav.hasNext}
        title="Next expandable block"
        aria-label="Next expandable block"
        className={BUTTON_CLASSES}
      >
        <ChevronDown className="size-3" />
      </button>
      <button
        type={BUTTON_TYPE}
        onClick={nav.goLast}
        disabled={!nav.hasNext}
        title="Jump to last expandable block"
        aria-label="Jump to last expandable block"
        className={BUTTON_CLASSES}
      >
        <ChevronLast className="size-3" />
      </button>
    </div>
  )
}
