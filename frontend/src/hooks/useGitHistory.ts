import { useState, useCallback, useEffect } from 'react'
import { getGitHistory } from '@/api/git'
import type { GitHistoryCommit } from '@/types/models'

/**
 * Loads the full unified commit history+graph from the backend
 * (GetGitHistory) in a single call. Replaces the separate useGitGraph
 * hook for the merged History tab. Returns the commits (sha, parents,
 * author, email, date, message, refs) plus loading/error state.
 */
export function useGitHistory() {
  const [commits, setCommits] = useState<GitHistoryCommit[]>([])
  // Start as `true` so the first paint shows the spinner, not the
  // "No commits yet" empty-state (load() runs in an effect after paint).
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getGitHistory()
      setCommits(result)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load history')
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return { commits, isLoading, error, reload: load }
}
