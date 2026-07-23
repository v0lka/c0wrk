import { useState, useMemo } from 'react'
import { MessageSquare, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useReviewStore, hunkCommentKey } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { emit } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { detectHljsLanguage, highlightCodeLine } from './hunkCodeHighlight'
import { parseHunkRaw } from './diffParsing'
import { UnifiedDiffTable } from './UnifiedDiffTable'
import { SplitDiffTable } from './SplitDiffTable'

interface HunkReviewBlockProps {
  sessionId: string
  filePath: string
  hunk: reviewApi.ReviewHunk
  hunkIndex: number
  /** Read-only mode: hide the comment button and inline comment editor. */
  readOnly?: boolean
}

export function HunkReviewBlock({ sessionId, filePath, hunk, hunkIndex, readOnly }: HunkReviewBlockProps) {
  const [showComment, setShowComment] = useState(false)
  const [draft, setDraft] = useState('')
  const hunkId = `hunk-${hunkIndex}`
  const commentKey = hunkCommentKey(filePath, hunkId)
  const comment = useReviewStore((s) => s.bySession[sessionId]?.hunkComments[commentKey] ?? '')
  const setHunkComment = useReviewStore((s) => s.setHunkComment)
  const diffViewMode = useReviewStore((s) => s.diffViewMode)

  const diffLines = useMemo(
    () => parseHunkRaw(hunk.raw, hunk.old_start, hunk.new_start),
    [hunk.raw, hunk.old_start, hunk.new_start],
  )

  // Detect the file's language once per file path, then highlight every
  // code line (add/del/context). Header and no-newline lines are escaped
  // as plain text (language = null) since they are not code.
  const language = useMemo(() => detectHljsLanguage(filePath), [filePath])
  const highlightedLines = useMemo(
    () =>
      diffLines.map((line) =>
        highlightCodeLine(
          line.text,
          line.type === 'add' || line.type === 'del' || line.type === 'context'
            ? language
            : null,
        ),
      ),
    [diffLines, language],
  )

  const openComment = () => {
    setDraft(comment)
    setShowComment(true)
  }

  const handleSave = async () => {
    const previous = comment
    setHunkComment(sessionId, filePath, hunkId, draft)
    setShowComment(false)
    try {
      await reviewApi.saveReviewHunkComment(sessionId, filePath, hunkId, draft)
    } catch (err) {
      logger.error('Failed to save hunk comment:', err)
      // Roll back the optimistic update so the UI matches the DB state.
      setHunkComment(sessionId, filePath, hunkId, previous)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to save hunk comment',
      })
    }
  }

  return (
    // `data-review-hunk` marks each hunk as a navigation target for the
    // prev/next chunk buttons in the review header (queried in document order).
    <div data-review-hunk="" className="border border-border/50 rounded-md overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-secondary/30 border-b border-border/50">
        <code className="text-xs text-muted-foreground">
          {filePath} · hunk {hunkIndex + 1}
        </code>
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

      {/* Diff block */}
      {diffViewMode === 'split' ? (
        <SplitDiffTable diffLines={diffLines} highlightedLines={highlightedLines} />
      ) : (
        <UnifiedDiffTable diffLines={diffLines} highlightedLines={highlightedLines} />
      )}

      {/* Inline comment */}
      {showComment && (
        <div className="border-t border-border/50 p-3 bg-secondary/20">
          <div className="flex items-start gap-2">
            <textarea
              className="flex-1 rounded-md border border-border bg-background p-2 text-xs resize-none"
              rows={2}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Leave a comment on this hunk..."
              autoFocus
            />
            <Button
              size="xs"
              onClick={handleSave}
              className="shrink-0"
            >
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

      {/* Resolved comment indicator */}
      {comment && !showComment && (
        <div className="border-t border-border/50 px-3 py-1.5 bg-warning/5">
          <p className="text-xs text-warning/80">{comment}</p>
        </div>
      )}
    </div>
  )
}
