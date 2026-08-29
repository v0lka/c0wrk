// ImageErrorBanner shows a dismissible warning when the user tries to attach
// image files while a non-vision model is selected. Reads the transient
// per-session `imageErrorBySession` slice for the ACTIVE session (keyed under
// the NULL_SESSION_KEY sentinel when no session is active, e.g. an image-only
// picker rejection before any session exists) and renders nothing when that
// session has no error. Styled per the design system (warning color #d19a66
// via Tailwind `text-warning` / `bg-warning` tokens).

import { AlertTriangle, X } from 'lucide-react'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { NULL_SESSION_KEY } from '@/stores/chatInputStore'

export function ImageErrorBanner(): React.JSX.Element | null {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const bannerKey = activeSessionId ?? NULL_SESSION_KEY
  const imageError = useAttachmentsStore((s) => s.imageErrorBySession[bannerKey])
  const setImageError = useAttachmentsStore((s) => s.setImageError)

  if (!imageError) return null

  return (
    <div className="flex items-center gap-2 px-3 py-1.5 shrink-0 border-b border-warning/30 bg-warning/5">
      <AlertTriangle className="size-3.5 shrink-0 text-warning" />
      <span className="text-xs text-warning flex-1 min-w-0">{imageError}</span>
      <button
        type="button"
        onClick={() => setImageError(bannerKey, null)}
        className="inline-flex items-center justify-center size-4 rounded-sm text-warning/70 hover:text-warning hover:bg-warning/10 shrink-0"
        title="Dismiss"
        aria-label="Dismiss image error"
      >
        <X className="size-3" />
      </button>
    </div>
  )
}
