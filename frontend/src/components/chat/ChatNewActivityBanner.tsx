import { ArrowDown } from 'lucide-react'

interface ChatNewActivityBannerProps {
  hasNewActivity: boolean
  scrollToBottom: () => void
}

export function ChatNewActivityBanner({
  hasNewActivity,
  scrollToBottom,
}: ChatNewActivityBannerProps) {
  if (!hasNewActivity) return null

  return (
    <button
      onClick={scrollToBottom}
      className="sticky bottom-2 left-1/2 -translate-x-1/2 z-10 px-3 py-1.5 rounded-full bg-primary text-primary-foreground text-xs shadow-lg hover:bg-primary/90 active:bg-primary/75 transition-colors flex items-center gap-1.5"
      aria-label="Jump to new activity"
    >
      <ArrowDown className="h-3 w-3" />
      <span>New activity</span>
    </button>
  )
}
