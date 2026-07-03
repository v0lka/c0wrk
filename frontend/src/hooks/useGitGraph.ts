import { useState, useCallback, useEffect } from 'react'
import { getGitGraph } from '@/api/git'
import type { GraphCommit } from '@/types/models'

/** Page size used for server-side graph pagination (FE-2 / B5). */
export const GRAPH_PAGE_SIZE = 25

/**
 * Loads the commit graph one page at a time from the backend
 * (GetGitGraph(limit, skip)) and appends older commits on "Load more".
 *
 * Extracted from GitGraph so the component stays focused on rendering.
 * Returns the accumulated commits plus load-more state and a refetch.
 */
export function useGitGraph() {
  const [commits, setCommits] = useState<GraphCommit[]>([])
  const [skip, setSkip] = useState(0)
  const [hasMore, setHasMore] = useState(true)
  const [isLoading, setIsLoading] = useState(false)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getGitGraph(GRAPH_PAGE_SIZE, 0)
      setCommits(result)
      setSkip(GRAPH_PAGE_SIZE)
      setHasMore(result.length >= GRAPH_PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load graph')
    } finally {
      setIsLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    if (isLoadingMore || !hasMore) return
    setIsLoadingMore(true)
    try {
      const result = await getGitGraph(GRAPH_PAGE_SIZE, skip)
      if (result.length > 0) {
        setCommits((prev) => [...prev, ...result])
      }
      setSkip((s) => s + GRAPH_PAGE_SIZE)
      // The backend returns fewer than the page size only when the log is exhausted.
      setHasMore(result.length >= GRAPH_PAGE_SIZE)
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
