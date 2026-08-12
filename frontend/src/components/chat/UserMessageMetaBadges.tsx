// UserMessageMetaBadges renders the read-only indicator row that appears above
// a user message's content: a goal badge, document attachment chips, and an
// image-thumbnail strip. All signals come from the unified user-message
// metadata blob parsed by `parseUserMessageMeta`; nothing here is editable.
//
// - Goal badge: a compact Target + "Goal" pill in the highlight color.
// - Doc chips: read-only FileText + name + (format) — mirrors AttachmentChips
//   styling but without size/remove controls (these are already-sent docs).
// - Image strip: 24px thumbnails; clicking opens the on-disk file in the file
//   viewer via an imperative store call (no reactive subscription needed).
//
// Renders `null` when there is nothing to show, so the parent layout stays
// unchanged for plain text messages.

import { useCallback } from 'react'
import { FileText, Target, Zap } from 'lucide-react'
import type { UserMessageMeta, StoredImageMeta } from '@/lib/userMessageMeta'
import { useFileViewerStore } from '@/stores/fileViewerStore'

interface UserMessageMetaBadgesProps {
  meta: UserMessageMeta
  isPinned?: boolean
}

/** Open a stored image's on-disk file in the file viewer. Imperative: no
 *  reactive store subscription is required for a fire-and-forget click. */
function useOpenImage(): (path: string) => void {
  return useCallback((path: string) => {
    if (path) useFileViewerStore.getState().openFile(path)
  }, [])
}

function GoalBadge(): React.JSX.Element {
  return (
    <span
      className="inline-flex items-center gap-1 h-6 px-2 rounded-md bg-highlight/10 text-xs font-medium text-[var(--color-highlight)] shrink-0"
      title="Goal"
    >
      <Target className="size-3 shrink-0" />
      <span>Goal</span>
    </span>
  )
}

function NudgeBadge(): React.JSX.Element {
  return (
    <span
      className="inline-flex items-center gap-1 h-6 px-2 rounded-md bg-info/10 text-xs font-medium text-info shrink-0"
      title="Nudge — sent while paused to resume the task"
    >
      <Zap className="size-3 shrink-0" />
      <span>Nudge</span>
    </span>
  )
}

function DocChip({
  originalName,
  format,
}: {
  originalName: string
  format: string
}): React.JSX.Element {
  return (
    <span
      className="inline-flex items-center gap-1 max-w-[220px] h-6 px-2 rounded-md border border-border bg-muted/40 text-xs text-foreground shrink-0"
      title={originalName}
    >
      <FileText className="size-3 shrink-0 text-muted-foreground" />
      <span className="truncate">{originalName}</span>
      <span className="text-muted-foreground shrink-0">({format})</span>
    </span>
  )
}

function ImageStrip({
  images,
  onOpen,
}: {
  images: StoredImageMeta[]
  onOpen: (path: string) => void
}): React.JSX.Element {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {images.map((img) => (
        <button
          key={img.id}
          type="button"
          onClick={() => onOpen(img.path)}
          className="shrink-0 rounded-sm overflow-hidden hover:ring-2 hover:ring-highlight/40 transition-shadow"
          title={img.name}
          aria-label={`Open ${img.name} in file viewer`}
        >
          <img
            src={img.thumbnail}
            alt={img.name}
            className="size-6 object-cover"
          />
        </button>
      ))}
    </div>
  )
}

export function UserMessageMetaBadges({
  meta,
  isPinned: _isPinned,
}: UserMessageMetaBadgesProps): React.JSX.Element | null {
  const hasGoal = meta.goal === true
  const hasNudge = meta.is_nudge === true
  const docs = meta.attachments ?? []
  const images = meta.images ?? []
  const openImage = useOpenImage()

  if (!hasGoal && !hasNudge && docs.length === 0 && images.length === 0) {
    return null
  }

  return (
    <div className="flex flex-col gap-1 shrink-0 mb-2">
      {(hasGoal || hasNudge || docs.length > 0) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {hasGoal && <GoalBadge />}
          {hasNudge && <NudgeBadge />}
          {docs.map((doc) => (
            <DocChip
              key={`${doc.original_name}-${doc.format}`}
              originalName={doc.original_name}
              format={doc.format}
            />
          ))}
        </div>
      )}
      {images.length > 0 && <ImageStrip images={images} onOpen={openImage} />}
    </div>
  )
}
