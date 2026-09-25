import { ArrowDown } from 'lucide-react'

interface ChatNewActivityBannerProps {
  hasNewActivity: boolean
  scrollToBottom: () => void
}

/**
 * The "New activity" pill — jumps the transcript viewport to the live tail.
 *
 * Stickiness lives in ChatScrollManager's single bottom stack wrapper (see
 * ChatScrollManager's render): this component is a plain inline-flex button
 * that renders inside it, stacked above the block-overflow toolbar. It
 * carries no positioning of its own, so it cannot overlap the toolbar.
 */
export function ChatNewActivityBanner({
  hasNewActivity,
  scrollToBottom,
}: ChatNewActivityBannerProps) {
  if (!hasNewActivity) return null

  return (
    <button
      onClick={scrollToBottom}
      className="pointer-events-auto px-3 py-1.5 rounded-full bg-primary text-primary-foreground text-xs shadow-lg hover:bg-primary/90 active:bg-primary/75 transition-colors flex items-center gap-1.5"
      aria-label="Jump to new activity"
    >
      <ArrowDown className="h-3 w-3" />
      <span>New activity</span>
    </button>
  )
}
