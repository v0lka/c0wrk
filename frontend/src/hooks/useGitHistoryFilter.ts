// File-filter logic for the git history tab.
//
// When a glob/regex filter is active, every loaded commit's changed files
// must be known to decide which commits to keep. The files themselves are
// fetched and cached by the shared useCommitFiles hook (one cache shared
// with the inline commit expansion, so expanding a filtered commit is
// instant); this hook only decides WHEN to fetch (all uncached commits once
// a valid filter is active) and which commits match. Matching reuses the
// shared createPathMatcher so semantics match the file-tree filter exactly.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { useCommitFiles } from './useCommitFiles'
import { createPathMatcher, isInvalidRegex, type FilterMode } from '@/lib/pathFilter'
import type { CommitFile, GitHistoryCommit } from '@/types/models'

interface UseGitHistoryFilterReturn {
  filterText: string
  filterMode: FilterMode
  setFilterText: (text: string) => void
  toggleFilterMode: () => void
  /** True when a non-empty filter is entered (the graph is hidden). */
  isFiltering: boolean
  /** True when the filter text is a syntactically invalid regex. */
  isInvalidFilter: boolean
  /** True while changed files are still being fetched for the filter. */
  isResolvingFiles: boolean
  /** Commits whose changed files match the filter (all commits when not filtering). */
  filteredCommits: GitHistoryCommit[]
  /** Per-SHA changed-files cache, shared with inline commit expansion. */
  filesBySha: Record<string, CommitFile[]>
  /** Fetch + cache a commit's files (deduped; no-op when already cached). */
  fetchFiles: (sha: string) => Promise<void>
  /** SHAs whose files are currently being fetched. */
  pendingShas: ReadonlySet<string>
}

export function useGitHistoryFilter(commits: GitHistoryCommit[]): UseGitHistoryFilterReturn {
  const [filterText, setFilterText] = useState('')
  const [filterMode, setFilterMode] = useState<FilterMode>('glob')
  const { filesBySha, fetchFiles, fetchMany, pendingShas } = useCommitFiles()

  const toggleFilterMode = useCallback(() => {
    setFilterMode((m) => (m === 'glob' ? 'regex' : 'glob'))
  }, [])

  const trimmed = filterText.trim()
  const isFiltering = trimmed.length > 0
  const isInvalidFilter = isInvalidRegex(filterText, filterMode)
  const matcher = useMemo(
    () => createPathMatcher(filterText, filterMode),
    [filterText, filterMode],
  )

  // When filtering, ensure every loaded commit's files are fetched so the
  // matcher can test them. Re-runs when the filter becomes valid (or the
  // loaded window grows); fetchMany dedupes per SHA against the cache and
  // in-flight requests, so the post-resolve re-runs below are no-ops.
  useEffect(() => {
    if (!isFiltering || matcher === null) return
    const uncached = commits
      .filter((c) => filesBySha[c.sha] === undefined)
      .map((c) => c.sha)
    if (uncached.length > 0) {
      void fetchMany(uncached)
    }
  }, [isFiltering, matcher, commits, filesBySha, fetchMany])

  const filteredCommits = useMemo(() => {
    if (!isFiltering) return commits
    if (matcher === null) return []
    return commits.filter((c) => {
      const files = filesBySha[c.sha]
      if (!files) return false // not yet resolved → excluded until fetched
      return files.some((f) => matcher(f.path))
    })
  }, [commits, isFiltering, matcher, filesBySha])

  const isResolvingFiles = isFiltering && matcher !== null && pendingShas.size > 0

  return {
    filterText,
    filterMode,
    setFilterText,
    toggleFilterMode,
    isFiltering,
    isInvalidFilter,
    isResolvingFiles,
    filteredCommits,
    filesBySha,
    fetchFiles,
    pendingShas,
  }
}
