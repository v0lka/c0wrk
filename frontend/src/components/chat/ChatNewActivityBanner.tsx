import React from 'react'

interface ChatNewActivityBannerProps {
  hasNewActivity: boolean
  isAtBottomRef: React.MutableRefObject<boolean>
  viewportRef: React.MutableRefObject<HTMLElement | null>
  setHasNewActivity: (value: boolean) => void
}

export function ChatNewActivityBanner({
  hasNewActivity,
  isAtBottomRef,
  viewportRef,
  setHasNewActivity,
}: ChatNewActivityBannerProps): React.ReactNode {
  if (!hasNewActivity || isAtBottomRef.current) return null

  return (
    <button
      onClick={() => {
        const viewport = viewportRef.current
        if (viewport) {
          viewport.scrollTop = viewport.scrollHeight
          isAtBottomRef.current = true
          setHasNewActivity(false)
        }
      }}
      className="sticky bottom-2 left-1/2 -translate-x-1/2 z-10 px-3 py-1.5 rounded-full bg-primary text-primary-foreground text-xs shadow-lg hover:bg-primary/90 active:bg-primary/75 transition-colors flex items-center gap-1.5"
      aria-label="Jump to new activity"
    >
      <span aria-hidden="true">↓</span>
      <span>New activity</span>
    </button>
  )
}
