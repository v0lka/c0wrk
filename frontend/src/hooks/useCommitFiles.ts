// Shared changed-files cache for commit-history UIs.
//
// Owns the per-SHA cache of a commit's changed files (GetCommitFiles) and
// the batch loader (GetCommitFilesBatch) previously inlined in
// useGitHistoryFilter, so the history filter and the inline commit expansion
// (and any future surface) share ONE cache and ONE pending-set instead of
// duplicating fetch/dedupe logic. Stateful but presentation-agnostic; all
// matching/filtering stays with the callers.

import { useCallback, useRef, useState } from 'react'
import { getCommitFiles, getCommitFilesBatch } from '@/api/git'
import type { CommitFile } from '@/types/models'

export interface UseCommitFilesReturn {
  /** Per-SHA changed-files cache. */
  filesBySha: Record<string, CommitFile[]>
  /** Fetch + cache one commit's files (deduped; no-op when already cached). */
  fetchFiles: (sha: string) => Promise<void>
  /** Batch-fetch + cache several commits' files in one backend RPC (deduped against the cache and in-flight requests). */
  fetchMany: (shas: string[]) => Promise<void>
  /** SHAs whose files are currently being fetched. */
  pendingShas: ReadonlySet<string>
}

export function useCommitFiles(): UseCommitFilesReturn {
  const [filesBySha, setFilesBySha] = useState<Record<string, CommitFile[]>>({})
  const [pendingShas, setPendingShas] = useState<ReadonlySet<string>>(new Set())

  // Ref mirror of the cache so the stable fetch callbacks can read the
  // latest cached state without re-creating (and re-triggering effects).
  const filesRef = useRef(filesBySha)
  filesRef.current = filesBySha
  // SHAs with an in-flight request; prevents duplicate concurrent fetches.
  const requestedRef = useRef<Set<string>>(new Set())

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
  const fetchMany = useCallback(async (shas: string[]) => {
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

  return { filesBySha, fetchFiles, fetchMany, pendingShas }
}
