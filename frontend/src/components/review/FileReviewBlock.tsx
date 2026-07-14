import { FileText } from 'lucide-react'
import type { ReviewFileDiff } from '@/api/review'
import { HunkReviewBlock } from './HunkReviewBlock'

interface FileReviewBlockProps {
  sessionId: string
  file: ReviewFileDiff
  /** Read-only mode: hide per-hunk comment controls. */
  readOnly?: boolean
}

export function FileReviewBlock({ sessionId, file, readOnly }: FileReviewBlockProps) {
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
      </div>
      {file.hunks.map((hunk, i) => (
        <HunkReviewBlock
          key={i}
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
