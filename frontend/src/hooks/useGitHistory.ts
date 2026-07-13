import { useState, useCallback, useEffect, useRef } from 'react'
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
 *
 * Concurrency note: `isLoadingMore` (React state) only reflects the
 * loading flag after a re-render, so it cannot guard against rapid
 * double/triple-clicks that fire before the button's `disabled`
 * attribute reaches the DOM. A ref (`isLoadingMoreRef`) provides a
 * synchronous guard that is set immediately on entry and cleared in
 * `finally`, preventing duplicate concurrent fetches that would fetch
 * the same page, add duplicate SHAs (silently discarded by React key
 * reconciliation), and advance `skip` multiple times — the exact
 * symptom where "Load more" appeared to require several clicks.
 */
export function useGitHistory() {
  const [commits, setCommits] = useState<GitHistoryCommit[]>([])
  const [hasMore, setHasMore] = useState(true)
  // Start as `true` so the first paint shows the spinner, not the
  // "No commits yet" empty-state (load() runs in an effect after paint).
  const [isLoading, setIsLoading] = useState(true)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Synchronous mirrors — readable inside the stable loadMore callback
  // without depending on state values that lag by one render.
  const isLoadingMoreRef = useRef(false)
  const skipRef = useRef(0)

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getGitHistory(HISTORY_PAGE_SIZE, 0)
      setCommits(result)
      skipRef.current = HISTORY_PAGE_SIZE
      setHasMore(result.length >= HISTORY_PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load history')
    } finally {
      setIsLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    // Ref guard is synchronous — prevents concurrent calls from rapid
    // clicks before the button's `disabled` attribute is applied.
    if (isLoadingMoreRef.current || !hasMore) return
    isLoadingMoreRef.current = true
    setIsLoadingMore(true)
    try {
      const result = await getGitHistory(HISTORY_PAGE_SIZE, skipRef.current)
      if (result.length > 0) {
        setCommits((prev) => [...prev, ...result])
      }
      skipRef.current += HISTORY_PAGE_SIZE
      // The backend returns fewer than the page size only when the log is exhausted.
      setHasMore(result.length >= HISTORY_PAGE_SIZE)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load more commits')
      setHasMore(false)
    } finally {
      isLoadingMoreRef.current = false
      setIsLoadingMore(false)
    }
  }, [hasMore])

  useEffect(() => {
    void load()
  }, [load])

  return { commits, hasMore, isLoading, isLoadingMore, error, loadMore }
}
