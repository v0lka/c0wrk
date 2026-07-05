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

  // Fetch entries for an active search. Empty query is intentionally not
  // fetched — the panel shows an empty-state placeholder when the user has
  // not entered a search (see VectorSearchResults). Stable reference because
  // all setters come from Zustand and are stable.
  const fetchEntries = useCallback(async (
    q: string,
    k: number,
    pattern: string,
    tokens: string[],
    m: SearchMode,
  ) => {
    if (q === '') {
      setEntries([])
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const results = await searchVectorStore({
        query: q,
        top_k: k,
        file_pattern: pattern,
        must_match: tokens,
        mode: m,
      })
      setEntries(results)
    } catch {
      setEntries([])
    } finally {
      setLoading(false)
    }
  }, [setEntries, setLoading])

  // Auto-search on mount and when inputs change — but only with an active
  // query. Empty query leaves the panel in the placeholder state.
  useEffect(() => {
    if (status.state === 'ready' && activeProjectId && query !== '') {
      fetchEntries(query, topK, filePattern, mustMatch, mode)
    }
  }, [status.state, activeProjectId, fetchEntries, query, topK, filePattern, mustMatch, mode])

  // Clear entries whenever query becomes empty so stale results do not
  // persist after the user deletes their search text or calls handleClear.
  useEffect(() => {
    if (query === '') {
      setEntries([])
    }
  }, [query, setEntries])

  // Reset entries when project changes
  useEffect(() => {
    if (prevProjectRef.current !== activeProjectId) {
      prevProjectRef.current = activeProjectId
      setEntries([])
      clearFilter()
    }
  }, [activeProjectId, setEntries, clearFilter])

  // Refresh the active search when the index reports it is ready (e.g. after
  // initial indexing or an incremental reindex). Without an active query this
  // is a no-op — we never auto-browse, so the empty-query placeholder stays.
  useEffect(() => {
    const unsub = subscribe('vector_index:status', (data: unknown) => {
      if (!isVectorIndexPayload(data) || data.state !== 'ready') return
      const s = useVectorIndexStore.getState()
      if (s.query === '') return
      fetchEntries(s.query, s.topK, s.filePattern, s.mustMatch, s.mode)
    })
    return unsub
  }, [fetchEntries])

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
    setEntries([])
  }, [clearFilter, setEntries])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSearch()
    }
  }, [handleSearch])

  const isSearchMode = query !== ''

  const statusMetaText =
    status.state !== 'ready' || !isSearchMode
      ? null
      : `${entries.length} entries · Search (${mode})`

  return { isSearchMode, statusMetaText, handleSearch, handleClear, handleKeyDown }
}
