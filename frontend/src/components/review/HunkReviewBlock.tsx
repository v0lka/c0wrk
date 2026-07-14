import { useState, useMemo } from 'react'
import { MessageSquare, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useReviewStore, hunkCommentKey } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { logger } from '@/lib/logger'

interface HunkReviewBlockProps {
  sessionId: string
  filePath: string
  hunk: reviewApi.ReviewHunk
  hunkIndex: number
  /** Read-only mode: hide the comment button and inline comment editor. */
  readOnly?: boolean
}

interface DiffLine {
  type: 'add' | 'del' | 'context' | 'header' | 'noNewline'
  text: string
  oldNum: number | null
  newNum: number | null
}

function parseHunkRaw(raw: string, oldStart: number, newStart: number): DiffLine[] {
  const lines = raw.split('\n')
  const result: DiffLine[] = []
  let oldNum = oldStart
  let newNum = newStart

  for (const line of lines) {
    if (line.startsWith('@@')) {
      result.push({ type: 'header', text: line, oldNum: null, newNum: null })
    } else if (line.startsWith('+')) {
      result.push({ type: 'add', text: line.slice(1), oldNum: null, newNum: newNum++ })
    } else if (line.startsWith('-')) {
      result.push({ type: 'del', text: line.slice(1), oldNum: oldNum++, newNum: null })
    } else if (line.startsWith(' ')) {
      result.push({ type: 'context', text: line.slice(1), oldNum: oldNum++, newNum: newNum++ })
    } else if (line === '') {
      result.push({ type: 'context', text: '', oldNum: oldNum++, newNum: newNum++ })
    } else if (line.startsWith('\\')) {
      result.push({ type: 'noNewline', text: 'No newline at end of file', oldNum: null, newNum: null })
    }
  }
  return result
}

const LINE_COLORS: Record<DiffLine['type'], string> = {
  add: 'bg-success/10 text-success',
  del: 'bg-destructive/10 text-destructive',
  context: '',
  header: 'bg-info/10 text-info font-medium',
  noNewline: 'text-muted-foreground/50 italic',
}

export function HunkReviewBlock({ sessionId, filePath, hunk, hunkIndex, readOnly }: HunkReviewBlockProps) {
  const [showComment, setShowComment] = useState(false)
  const [draft, setDraft] = useState('')
  const hunkId = `hunk-${hunkIndex}`
  const commentKey = hunkCommentKey(filePath, hunkId)
  const comment = useReviewStore((s) => s.bySession[sessionId]?.hunkComments[commentKey] ?? '')
  const setHunkComment = useReviewStore((s) => s.setHunkComment)

  const diffLines = useMemo(
    () => parseHunkRaw(hunk.raw, hunk.old_start, hunk.new_start),
    [hunk.raw, hunk.old_start, hunk.new_start],
  )

  const openComment = () => {
    setDraft(comment)
    setShowComment(true)
  }

  const handleSave = () => {
    setHunkComment(sessionId, filePath, hunkId, draft)
    reviewApi.saveReviewHunkComment(sessionId, filePath, hunkId, draft).catch((err) => {
      logger.error('Failed to save hunk comment:', err)
    })
    setShowComment(false)
  }

  return (
    <div className="border border-border/50 rounded-md overflow-hidden">
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
      <div className="overflow-x-auto font-mono text-xs leading-relaxed">
        <table className="w-full border-collapse">
          <tbody>
            {diffLines.map((line, i) => (
              <tr key={i} className={LINE_COLORS[line.type]}>
                <td className="select-none text-right pr-2 pl-2 text-muted-foreground/50 w-10 tabular-nums align-top">
                  {line.oldNum ?? ''}
                </td>
                <td className="select-none text-right pr-2 text-muted-foreground/50 w-10 tabular-nums align-top">
                  {line.newNum ?? ''}
                </td>
                <td className="pr-3 whitespace-pre-wrap break-all">
                  {line.type === 'header' ? line.text : line.text || ' '}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

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
