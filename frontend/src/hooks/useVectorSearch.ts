import { useEffect, useRef, useCallback } from 'react'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectStore } from '@/stores/projectStore'
import { searchVectorStore } from '@/api/vector'
import { subscribe } from '@/api/runtime'
import { isVectorIndexPayload } from '@/types/events'
import { parsePlusTokens } from '@/lib/plusTokens'
import type { SearchMode } from '@/types/models'

export interface UseVectorSearchResult {
  isSearchMode: boolean
  statusMetaText: string | null
  handleSearch: () => void
  handleClear: () => void
  handleKeyDown: (e: React.KeyboardEvent) => void
}

/**
 * useVectorSearch wires VectorStorePanel's data flow: project-change reset,
 * vector_index:status subscription, and the search/clear/keypress handlers.
 * The component reads input/result state directly from useVectorIndexStore
 * and forwards them to the filter/results child components. (W-30 split)
 */
export function useVectorSearch(): UseVectorSearchResult {
  const status = useVectorIndexStore((s) => s.status)
  const entries = useVectorIndexStore((s) => s.entries)
  const query = useVectorIndexStore((s) => s.query)
  const topK = useVectorIndexStore((s) => s.topK)
  const filePattern = useVectorIndexStore((s) => s.filePattern)
  const mustMatch = useVectorIndexStore((s) => s.mustMatch)
  const mode = useVectorIndexStore((s) => s.mode)
  const setEntries = useVectorIndexStore((s) => s.setEntries)
  const setLoading = useVectorIndexStore((s) => s.setLoading)
  const setQuery = useVectorIndexStore((s) => s.setQuery)
  const setMustMatch = useVectorIndexStore((s) => s.setMustMatch)
  const clearFilter = useVectorIndexStore((s) => s.clearFilter)

  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const prevProjectRef = useRef(activeProjectId)

  // Fetch entries (browse or search). Stable reference because all setters
  // come from Zustand and are stable.
  const fetchEntries = useCallback(async (
    q: string,
    k: number,
    pattern: string,
    tokens: string[],
    m: SearchMode,
  ) => {
    setLoading(true)
    try {
      const results = await searchVectorStore({
        query: q,
        top_k: k,
        file_pattern: pattern,
        must_match: tokens,
        mode: q === '' ? '' : m,
      })
      setEntries(results)
    } catch {
      setEntries([])
    } finally {
      setLoading(false)
    }
  }, [setEntries, setLoading])

  // Auto-browse on mount and when index becomes ready
  useEffect(() => {
    if (status.state === 'ready' && activeProjectId) {
      fetchEntries(query, topK, filePattern, mustMatch, mode)
    }
  }, [status.state, activeProjectId, fetchEntries, query, topK, filePattern, mustMatch, mode])

  // Reset entries when project changes
  useEffect(() => {
    if (prevProjectRef.current !== activeProjectId) {
      prevProjectRef.current = activeProjectId
      setEntries([])
      clearFilter()
    }
  }, [activeProjectId, setEntries, clearFilter])

  // Re-fetch on vector_index:status ready event
  useEffect(() => {
    const unsub = subscribe('vector_index:status', (data: unknown) => {
      if (isVectorIndexPayload(data) && data.state === 'ready') {
        fetchEntries('', topK, '', [], mode)
      }
    })
    return unsub
  }, [fetchEntries, topK, mode])

  const handleSearch = useCallback(() => {
    // Strip +tokens from query and merge into mustMatch before search
    const parsed = parsePlusTokens(query)
    let finalTokens = mustMatch
    if (parsed.tokens.length > 0) {
      const merged = [...mustMatch]
      for (const tok of parsed.tokens) {
        if (!merged.includes(tok)) merged.push(tok)
      }
      finalTokens = merged
      setQuery(parsed.query)
      setMustMatch(merged)
    }
    fetchEntries(parsed.query, topK, filePattern, finalTokens, mode)
  }, [fetchEntries, query, topK, filePattern, mustMatch, mode, setQuery, setMustMatch])

  const handleClear = useCallback(() => {
    clearFilter()
    fetchEntries('', topK, '', [], mode)
  }, [clearFilter, fetchEntries, topK, mode])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSearch()
    }
  }, [handleSearch])

  const isSearchMode = query !== ''

  const statusMetaText =
    status.state !== 'ready'
      ? null
      : `${entries.length} entries · ${isSearchMode ? `Search (${mode})` : 'Browse'}`

  return { isSearchMode, statusMetaText, handleSearch, handleClear, handleKeyDown }
}
