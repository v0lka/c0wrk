// File-filter logic for the git history tab.
//
// When a glob/regex filter is active, every loaded commit's changed files
// must be known to decide which commits to keep. Files are fetched lazily
// (GetCommitFiles) and cached per SHA; the cache is shared with the inline
// commit expansion so expanding a filtered commit is instant. Matching
// reuses the shared createPathMatcher so semantics match the file-tree
// filter exactly.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getCommitFiles, getCommitFilesBatch } from '@/api/git'
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
  const [filesBySha, setFilesBySha] = useState<Record<string, CommitFile[]>>({})
  const [pendingShas, setPendingShas] = useState<ReadonlySet<string>>(new Set())

  // Ref mirror of the cache so the stable fetchFiles callback can read the
  // latest cached state without re-creating (and re-triggering effects).
  const filesRef = useRef(filesBySha)
  filesRef.current = filesBySha
  // SHAs with an in-flight request; prevents duplicate concurrent fetches.
  const requestedRef = useRef<Set<string>>(new Set())

  const toggleFilterMode = useCallback(() => {
    setFilterMode((m) => (m === 'glob' ? 'regex' : 'glob'))
  }, [])

  const fetchFiles = useCallback(async (sha: string) => {
    if (filesRef.current[sha] !== undefined) return
    if (requestedRef.current.has(sha)) return
    requestedRef.current.add(sha)
    setPendingShas((prev) => new Set(prev).add(sha))
    try {
      const files = await getCommitFiles(sha)
      setFilesBySha((prev) => ({ ...prev, [sha]: files }))
    } catch {
      setFilesBySha((prev) => ({ ...prev, [sha]: [] }))
    } finally {
      requestedRef.current.delete(sha)
      setPendingShas((prev) => {
        const next = new Set(prev)
        next.delete(sha)
        return next
      })
    }
  }, [])

  // Batch fetch: resolves all uncached SHAs in a single backend RPC
  // (GetCommitFilesBatch) instead of N concurrent getCommitFiles calls.
  // Dedupes against the cache and in-flight requests just like fetchFiles.
  const fetchFilesBatch = useCallback(async (shas: string[]) => {
    const toFetch: string[] = []
    for (const sha of shas) {
      if (filesRef.current[sha] === undefined && !requestedRef.current.has(sha)) {
        toFetch.push(sha)
      }
    }
    if (toFetch.length === 0) return

    for (const sha of toFetch) {
      requestedRef.current.add(sha)
    }
    setPendingShas((prev) => {
      const next = new Set(prev)
      for (const sha of toFetch) next.add(sha)
      return next
    })

    try {
      const result = await getCommitFilesBatch(toFetch)
      setFilesBySha((prev) => {
        const next = { ...prev }
        for (const sha of toFetch) {
          next[sha] = result[sha] ?? []
        }
        return next
      })
    } catch {
      setFilesBySha((prev) => {
        const next = { ...prev }
        for (const sha of toFetch) {
          next[sha] = []
        }
        return next
      })
    } finally {
      for (const sha of toFetch) {
        requestedRef.current.delete(sha)
      }
      setPendingShas((prev) => {
        const next = new Set(prev)
        for (const sha of toFetch) next.delete(sha)
        return next
      })
    }
  }, [])

  const trimmed = filterText.trim()
  const isFiltering = trimmed.length > 0
  const isInvalidFilter = isInvalidRegex(filterText, filterMode)
  const matcher = useMemo(
    () => createPathMatcher(filterText, filterMode),
    [filterText, filterMode],
  )

  // When filtering, ensure every loaded commit's files are fetched so the
  // matcher can test them. Re-runs when more commits are loaded (Load more)
  // or when the filter becomes valid; fetchFilesBatch dedupes per SHA.
  useEffect(() => {
    if (!isFiltering || matcher === null) return
    const uncached = commits
      .filter((c) => filesRef.current[c.sha] === undefined)
      .map((c) => c.sha)
    if (uncached.length > 0) {
      void fetchFilesBatch(uncached)
    }
  }, [isFiltering, matcher, commits, fetchFilesBatch])

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
