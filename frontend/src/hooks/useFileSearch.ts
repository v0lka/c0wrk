// Hook for file tree search/filter logic with debounced glob/regex matching.

import { useCallback, useEffect, useRef } from 'react'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { listDirectory } from '@/api/workspace'
import picomatch from 'picomatch'

interface UseFileSearchReturn {
  filterText: string
  filterMode: 'glob' | 'regex'
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

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

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
        const entries = await listDirectory(rootPath, true)
        const trimmed = value.trim()
        const pattern = filterMode === 'regex' ? new RegExp(trimmed, 'i') : null
        const isMatch = !pattern ? picomatch(trimmed, { nocase: true, contains: true }) : null
        const filtered = entries.filter((e) => {
          if (e.is_dir) return false
          if (pattern) return pattern.test(e.name)
          if (isMatch) return isMatch(e.name) || isMatch(e.path)
          return false
        })
        setSearchEntries(filtered)
      } catch {
        setSearchEntries([])
      } finally {
        setIsSearching(false)
      }
    }, 300)
  }, [filterMode, rootPath, setFilterText, setSearchEntries, setIsSearching])

  const toggleFilterMode = useCallback(() => {
    setFilterMode(filterMode === 'glob' ? 'regex' : 'glob')
  }, [filterMode, setFilterMode])

  // Cleanup debounce on unmount
  useEffect(() => {
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current) }
  }, [])

  return { filterText, filterMode, handleFilterChange, toggleFilterMode }
}
