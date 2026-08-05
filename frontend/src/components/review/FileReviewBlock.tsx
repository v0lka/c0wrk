import { useState } from 'react'
import { FileText, MessageSquare, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useReviewStore } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { emit } from '@/api/runtime'
import { logger } from '@/lib/logger'
import type { ReviewFileDiff } from '@/api/review'
import { HunkReviewBlock } from './HunkReviewBlock'

interface FileReviewBlockProps {
  sessionId: string
  file: ReviewFileDiff
  /** Read-only mode: hide per-file and per-hunk comment controls. */
  readOnly?: boolean
}

export function FileReviewBlock({ sessionId, file, readOnly }: FileReviewBlockProps) {
  const [showComment, setShowComment] = useState(false)
  const [draft, setDraft] = useState('')
  const comment = useReviewStore((s) => s.bySession[sessionId]?.fileComments[file.path] ?? '')
  const setFileComment = useReviewStore((s) => s.setFileComment)

  const openComment = () => {
    setDraft(comment)
    setShowComment(true)
  }

  const handleSave = async () => {
    const previous = comment
    setFileComment(sessionId, file.path, draft)
    setShowComment(false)
    try {
      await reviewApi.saveReviewFileComment(sessionId, file.path, draft)
    } catch (err) {
      logger.error('Failed to save file comment:', err)
      // Roll back the optimistic update so the UI matches the DB state.
      setFileComment(sessionId, file.path, previous)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to save file comment',
      })
    }
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5 px-1">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <code className="text-sm font-medium text-foreground">
          {file.path}
        </code>
        {file.old_path && file.old_path !== file.path && (
          <span className="text-xs text-muted-foreground">
            (was {file.old_path})
          </span>
        )}
        <span className="text-xs text-muted-foreground ml-auto">
          {file.hunks.length} hunk{file.hunks.length !== 1 ? 's' : ''}
        </span>
        {!readOnly && (
          <Button
            variant="ghost"
            size="xs"
            onClick={() => (showComment ? setShowComment(false) : openComment())}
            className="text-xs"
          >
            <MessageSquare className="h-3 w-3 mr-1" />
            {comment ? 'Edit' : 'Comment'}
          </Button>
        )}
      </div>

      {/* Inline file comment editor (visible in both unified/split modes
          because it sits between the file header and the hunks). */}
      {showComment && !readOnly && (
        <div className="px-1">
          <div className="flex items-start gap-2 rounded-md border border-border bg-secondary/20 p-3">
            <textarea
              className="flex-1 rounded-md border border-border bg-background p-2 text-xs resize-none custom-scrollbar"
              rows={2}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Leave a comment on this file..."
              autoFocus
            />
            <Button size="xs" onClick={handleSave} className="shrink-0">
              Save
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => setShowComment(false)}
              title="Close"
            >
              <X className="h-3 w-3" />
            </Button>
          </div>
        </div>
      )}

      {/* Saved file comment indicator */}
      {comment && !showComment && (
        <div className="mx-1 rounded-md bg-warning/5 px-3 py-1.5">
          <p className="text-xs text-warning/80">{comment}</p>
        </div>
      )}

      {file.hunks.map((hunk, i) => (
        <HunkReviewBlock
          key={`${hunk.old_start}-${hunk.new_start}`}
          sessionId={sessionId}
          filePath={file.path}
          hunk={hunk}
          hunkIndex={i}
          readOnly={readOnly}
        />
      ))}
    </div>
  )
}
