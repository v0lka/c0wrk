import { useState, useCallback, useEffect, useRef } from 'react'
import { getGitHistory, GIT_HISTORY_PAGE_SIZE } from '@/api/git'
import type { GitHistoryCommit } from '@/types/models'

/**
 * Loads the unified commit history+graph from the backend
 * (`GetGitHistory`) page by page. The first page loads eagerly; `loadMore`
 * appends the next page (requested at the previous page's `next_skip`) and
 * is a no-op once `hasMore` is false. Replaces the earlier separate
 * useGitGraph hook; its only consumer is the viewer's Git history page.
 *
 * Appending (rather than replacing) the accumulated commits is deliberate:
 * already-rendered rows keep their identity and per-row UI state (e.g. an
 * expanded commit), and the virtualizer is not forced to rebuild the list.
 */
export function useGitHistory() {
  const [commits, setCommits] = useState<GitHistoryCommit[]>([])
  // Start as `true` so the first paint shows the spinner, not the
  // "No commits yet" empty-state (load() runs in an effect after paint).
  const [isLoading, setIsLoading] = useState(true)
  // True only while an additional page is in flight (drives the
  // "Load more" spinner without blanking the already-visible list).
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Offset of the next page, tracked in a ref so `loadMore` reads the
  // latest value without being re-created on every page.
  const nextSkipRef = useRef(0)
  // Guards against overlapping loadMore calls (scroll + button, rapid fires).
  const loadingMoreRef = useRef(false)
  // Epoch token: a (re)load bumps it so an in-flight loadMore from the
  // previous epoch cannot append stale commits after a refresh.
  const epochRef = useRef(0)

  const load = useCallback(async () => {
    epochRef.current += 1
    const epoch = epochRef.current
    setIsLoading(true)
    setIsLoadingMore(false)
    setError(null)
    loadingMoreRef.current = false
    try {
      const page = await getGitHistory(GIT_HISTORY_PAGE_SIZE, 0)
      if (epoch !== epochRef.current) return
      setCommits(page.commits)
      nextSkipRef.current = page.next_skip
      setHasMore(page.has_more)
    } catch (err) {
      if (epoch !== epochRef.current) return
      setError(err instanceof Error ? err.message : 'Failed to load history')
    } finally {
      if (epoch === epochRef.current) setIsLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    // Skip when already loading, when the list is exhausted, or when a
    // first page is still loading (nextSkipRef is not yet meaningful).
    if (loadingMoreRef.current || !hasMore) return
    loadingMoreRef.current = true
    setIsLoadingMore(true)
    const epoch = epochRef.current
    try {
      const page = await getGitHistory(GIT_HISTORY_PAGE_SIZE, nextSkipRef.current)
      if (epoch !== epochRef.current) return
      // Append — never replace — so loaded commits are preserved.
      setCommits((prev) => [...prev, ...page.commits])
      nextSkipRef.current = page.next_skip
      setHasMore(page.has_more)
    } catch (err) {
      if (epoch !== epochRef.current) return
      setError(err instanceof Error ? err.message : 'Failed to load more history')
    } finally {
      if (epoch === epochRef.current) {
        setIsLoadingMore(false)
        loadingMoreRef.current = false
      }
    }
  }, [hasMore])

  useEffect(() => {
    void load()
  }, [load])

  return { commits, isLoading, isLoadingMore, hasMore, error, reload: load, loadMore }
}
