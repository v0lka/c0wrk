import { useState, useEffect, useCallback } from 'react'
import { Loader2, RefreshCw, GitCommit } from 'lucide-react'
import { Button } from '@/components/ui/button'
import * as reviewApi from '@/api/review'
import { useReviewStore } from '@/stores/reviewStore'
import { ReviewHeader } from './ReviewHeader'
import { FileReviewBlock } from './FileReviewBlock'

interface ReviewPageProps {
  /** Active session id — required for the interactive working-tree review. */
  sessionId?: string
  /** Commit SHA — when set, renders a read-only view of that commit's diff. */
  commitSha?: string
}

export function ReviewPage({ sessionId, commitSha }: ReviewPageProps) {
  const readOnly = !!commitSha
  const [diff, setDiff] = useState<reviewApi.ReviewFileDiff[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const loadReview = useReviewStore((s) => s.loadReview)

  const fetchDiff = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const result = commitSha
        ? await reviewApi.getCommitDiff(commitSha)
        : await reviewApi.getReviewDiff()
      setDiff(result)
    } catch (err) {
      setError(commitSha ? 'Failed to load commit diff' : 'Failed to load review diff')
      console.error('fetchDiff failed:', err)
    } finally {
      setLoading(false)
    }
  }, [commitSha])

  useEffect(() => {
    void fetchDiff()
    // Only load the persisted review buffer (comments/status) in interactive
    // mode — a read-only commit view has no associated review buffer.
    if (!readOnly && sessionId) {
      void loadReview(sessionId)
    }
  }, [fetchDiff, loadReview, sessionId, readOnly])

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center gap-2">
        <p className="text-sm text-destructive">{error}</p>
        <Button size="xs" onClick={() => void fetchDiff()}>
          <RefreshCw className="h-3 w-3 mr-1" />Retry
        </Button>
      </div>
    )
  }

  if (diff.length === 0) {
    return (
      <div className="flex-1 flex flex-col">
        <ReviewHeader sessionId={sessionId ?? ''} readOnly={readOnly} commitSha={commitSha} />
        <div className="flex-1 flex flex-col items-center justify-center gap-2 text-muted-foreground">
          <GitCommit className="h-8 w-8 opacity-50" />
          <p className="text-sm">
            {readOnly ? 'No changes to display' : 'No uncommitted changes to review'}
          </p>
          <Button size="xs" variant="ghost" onClick={() => void fetchDiff()}>
            <RefreshCw className="h-3 w-3 mr-1" />Refresh
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <ReviewHeader sessionId={sessionId ?? ''} readOnly={readOnly} commitSha={commitSha} />
      <div className="flex-1 overflow-y-auto custom-scrollbar p-3 space-y-4">
        {diff.map((file) => (
          <FileReviewBlock
            key={file.path}
            sessionId={sessionId ?? ''}
            file={file}
            readOnly={readOnly}
          />
        ))}
      </div>
    </div>
  )
}
