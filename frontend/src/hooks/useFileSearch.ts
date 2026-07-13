// Hook for file tree search/filter logic with debounced glob/regex matching.
//
// Performance: the recursive directory listing is cached on the file-tree
// store (`flatEntries`/`flatEntriesRoot`) and reused for every keystroke
// instead of issuing a fresh listDirectory RPC each time. The cache is
// invalidated on `workspace:tree_changed` events and on root path changes.

import { useCallback, useEffect, useRef } from 'react'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { listDirectory } from '@/api/workspace'
import { subscribe } from '@/api/runtime'
import { createPathMatcher, isInvalidRegex, type FilterMode } from '@/lib/pathFilter'
import { DEBOUNCE_SEARCH_MS } from '@/constants/timing'
import type { FileEntry } from '@/types/models'

interface UseFileSearchReturn {
  filterText: string
  filterMode: FilterMode
  isInvalidFilter: boolean
  handleFilterChange: (value: string) => void
  toggleFilterMode: () => void
}

export function useFileSearch(): UseFileSearchReturn {
  const rootPath = useFileTreeStore((s) => s.rootPath)
  const filterText = useFileTreeStore((s) => s.filterText)
  const filterMode = useFileTreeStore((s) => s.filterMode)
  const setFilterText = useFileTreeStore((s) => s.setFilterText)
  const setFilterMode = useFileTreeStore((s) => s.setFilterMode)
  const setSearchEntries = useFileTreeStore((s) => s.setSearchEntries)
  const setIsSearching = useFileTreeStore((s) => s.setIsSearching)
  const setFlatEntries = useFileTreeStore((s) => s.setFlatEntries)
  const clearFlatEntries = useFileTreeStore((s) => s.clearFlatEntries)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Returns the cached flat listing for the active root, or fetches it once
  // (caching the result on the store) before returning.
  const ensureFlatEntries = useCallback(async (): Promise<FileEntry[]> => {
    const { flatEntries, flatEntriesRoot } = useFileTreeStore.getState()
    if (flatEntries.length > 0 && flatEntriesRoot === rootPath && rootPath) {
      return flatEntries
    }
    if (!rootPath) return []
    const entries = await listDirectory(rootPath, true)
    setFlatEntries(rootPath, entries)
    return entries
  }, [rootPath, setFlatEntries])

  const handleFilterChange = useCallback((value: string) => {
    setFilterText(value)
    if (debounceRef.current) clearTimeout(debounceRef.current)

    if (!value.trim()) {
      setSearchEntries([])
      setIsSearching(false)
      return
    }

    debounceRef.current = setTimeout(async () => {
      if (!rootPath) return
      setIsSearching(true)
      try {
        const entries = await ensureFlatEntries()
        const matcher = createPathMatcher(value, filterMode)
        const filtered = matcher ? entries.filter((e) => !e.is_dir && matcher(e.path)) : []
        setSearchEntries(filtered)
      } catch {
        setSearchEntries([])
      } finally {
        setIsSearching(false)
      }
    }, DEBOUNCE_SEARCH_MS)
  }, [filterMode, rootPath, ensureFlatEntries, setFilterText, setSearchEntries, setIsSearching])

  const toggleFilterMode = useCallback(() => {
    setFilterMode(filterMode === 'glob' ? 'regex' : 'glob')
  }, [filterMode, setFilterMode])

  // Invalidate the flat cache when the workspace tree changes so the next
  // search keystroke refetches once. Subscribed once per hook instance.
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      clearFlatEntries()
    })
    return unsub
  }, [clearFlatEntries])

  // Cleanup debounce on unmount
  useEffect(() => {
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current) }
  }, [])

  const isInvalidFilter = isInvalidRegex(filterText, filterMode)

  return { filterText, filterMode, isInvalidFilter, handleFilterChange, toggleFilterMode }
}
