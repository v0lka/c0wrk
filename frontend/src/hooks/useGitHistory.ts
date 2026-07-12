import { useState, useCallback, useEffect } from 'react'
import { getGitHistory } from '@/api/git'
import type { GitHistoryCommit } from '@/types/models'

/** Page size used for server-side history+graph pagination. */
export const HISTORY_PAGE_SIZE = 25

/**
 * Loads the unified commit history+graph one page at a time from the
 * backend (GetGitHistory(limit, skip)) and appends older commits on
 * "Load more".
 *
 * Replaces the separate useGitGraph hook for the merged History tab.
 * Returns the accumulated commits (sha, parents, author, email, date,
 * message, refs) plus load-more state.
 */
export function useGitHistory() {
  const [commits, setCommits] = useState<GitHistoryCommit[]>([])
  const [skip, setSkip] = useState(0)
  const [hasMore, setHasMore] = useState(true)
  const [isLoading, setIsLoading] = useState(false)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getGitHistory(HISTORY_PAGE_SIZE, 0)
      setCommits(result)
      setSkip(HISTORY_PAGE_SIZE)
      setHasMore(result.length >= HISTORY_PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load history')
    } finally {
      setIsLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    if (isLoadingMore || !hasMore) return
    setIsLoadingMore(true)
    try {
      const result = await getGitHistory(HISTORY_PAGE_SIZE, skip)
      if (result.length > 0) {
        setCommits((prev) => [...prev, ...result])
      }
      setSkip((s) => s + HISTORY_PAGE_SIZE)
      // The backend returns fewer than the page size only when the log is exhausted.
      setHasMore(result.length >= HISTORY_PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load more commits')
      setHasMore(false)
    } finally {
      setIsLoadingMore(false)
    }
  }, [skip, hasMore, isLoadingMore])

  useEffect(() => {
    void load()
  }, [load])

  return { commits, hasMore, isLoading, isLoadingMore, error, loadMore }
}
