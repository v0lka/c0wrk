import { useState, useEffect, useRef, useCallback } from 'react'

interface UseDropdownResult {
  isOpen: boolean
  setIsOpen: (open: boolean | ((prev: boolean) => boolean)) => void
  /** Attach to the dropdown container to detect outside clicks. */
  containerRef: React.RefObject<HTMLDivElement | null>
}

/**
 * Shared dropdown open/close state with outside-click dismissal.
 * Extracted from ModelCombobox and ReasoningCombobox.
 */
export function useDropdown(): UseDropdownResult {
  const [isOpen, setIsOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  const handleClickOutside = useCallback((e: MouseEvent) => {
    if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
      setIsOpen(false)
    }
  }, [])

  useEffect(() => {
    if (!isOpen) return
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [isOpen, handleClickOutside])

  return { isOpen, setIsOpen, containerRef }
}
