import { useState, useEffect, useCallback } from 'react'
import { Loader2, AlertCircle } from 'lucide-react'
import { getCommitLog, getCommitFiles } from '@/api/git'
import { CommitRow } from './GitCommitRow'
import type { CommitInfo, CommitFile } from '@/types/models'

const PAGE_SIZE = 25

/**
 * Commit history tab (Phase 5). Paginates `GetCommitLog`; clicking a commit
 * lazily expands its changed files via `GetCommitFiles`.
 */
export function GitHistoryTab() {
  const [commits, setCommits] = useState<CommitInfo[]>([])
  const [skip, setSkip] = useState(0)
  const [isLoading, setIsLoading] = useState(false)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasMore, setHasMore] = useState(true)
  const [expandedSha, setExpandedSha] = useState<string | null>(null)
  const [filesBySha, setFilesBySha] = useState<Record<string, CommitFile[]>>({})
  const [loadingFilesSha, setLoadingFilesSha] = useState<string | null>(null)

  const loadInitial = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getCommitLog(PAGE_SIZE, 0)
      setCommits(result)
      setSkip(PAGE_SIZE)
      setHasMore(result.length === PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load history')
    } finally {
      setIsLoading(false)
    }
  }, [])

  // Initial load on mount
  useEffect(() => {
    void loadInitial()
  }, [loadInitial])

  const handleLoadMore = useCallback(async () => {
    setIsLoadingMore(true)
    setError(null)
    try {
      const result = await getCommitLog(PAGE_SIZE, skip)
      setCommits((prev) => [...prev, ...result])
      setSkip((prev) => prev + PAGE_SIZE)
      setHasMore(result.length === PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load more commits')
    } finally {
      setIsLoadingMore(false)
    }
  }, [skip])

  const handleCommitClick = useCallback(
    async (sha: string) => {
      if (expandedSha === sha) {
        setExpandedSha(null)
        return
      }
      setExpandedSha(sha)
      if (filesBySha[sha] !== undefined) return // already fetched
      setLoadingFilesSha(sha)
      try {
        const files = await getCommitFiles(sha)
        setFilesBySha((prev) => ({ ...prev, [sha]: files }))
      } catch {
        setFilesBySha((prev) => ({ ...prev, [sha]: [] }))
      } finally {
        setLoadingFilesSha(null)
      }
    },
    [expandedSha, filesBySha],
  )

  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error && commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0 px-4 text-center">
        <div className="flex flex-col items-center gap-2 text-muted-foreground">
          <AlertCircle className="size-6 opacity-50" />
          <span className="text-xs">{error}</span>
        </div>
      </div>
    )
  }

  if (commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <span className="text-sm text-muted-foreground select-none">No commits yet</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-0 flex-1 overflow-y-auto">
      {commits.map((commit) => (
        <CommitRow
          key={commit.sha}
          commit={commit}
          expanded={expandedSha === commit.sha}
          files={filesBySha[commit.sha]}
          loadingFiles={loadingFilesSha === commit.sha}
          onClick={() => void handleCommitClick(commit.sha)}
        />
      ))}
      {hasMore && (
        <button
          type="button"
          onClick={() => void handleLoadMore()}
          disabled={isLoadingMore}
          className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
        >
          {isLoadingMore && <Loader2 className="size-3 animate-spin" />}
          {isLoadingMore ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  )
}
