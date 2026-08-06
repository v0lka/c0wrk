// DropzoneOverlay — full-window highlight shown while a file is being dragged
// over the app (native OS drag-and-drop). Purely presentational; the drag-state
// signal (dragActive) comes from useFileDrop, and the actual staging happens via
// the Wails `files:dropped` event that useFileDrop subscribes to.
//
// The overlay is a `fixed` full-screen layer with a dashed accent border and an
// UploadCloud glyph + caption, so the user sees a clear drop affordance wherever
// Wails delivers the drop. It renders null when not active so it never
// intercepts pointer events in the normal (non-dragging) state.

import { UploadCloud } from 'lucide-react'
import { cn } from '@/lib/utils'

interface DropzoneOverlayProps {
  /** Whether a file is currently being dragged over the window. */
  active: boolean
  /** Optional className override (e.g. to scope the overlay to a container). */
  className?: string
}

/**
 * Full-window drop-zone highlight. Renders nothing when `active` is false.
 */
export function DropzoneOverlay({ active, className }: DropzoneOverlayProps): React.JSX.Element | null {
  if (!active) return null

  return (
    <div
      aria-hidden
      className={cn(
        // Fixed so it covers the whole window regardless of where in the tree
        // it's mounted. z-index sits above panels but below toasts/dialogs.
        'fixed inset-0 z-30 flex items-center justify-center pointer-events-none',
        'bg-primary/5 backdrop-blur-[1px]',
        className,
      )}
    >
      <div
        className={cn(
          'flex flex-col items-center gap-3 rounded-xl border-2 border-dashed border-primary/60',
          'bg-card/80 px-10 py-8 shadow-lg',
        )}
      >
        <UploadCloud className="size-10 text-primary" />
        <span className="text-sm font-medium text-foreground">
          Drop to attach files
        </span>
        <span className="text-xs text-muted-foreground">
          Images and documents will be added to your message
        </span>
      </div>
    </div>
  )
}
