import { useState, useEffect, useRef, useCallback } from 'react'

interface UseDropdownResult {
  isOpen: boolean
  setIsOpen: (open: boolean | ((prev: boolean) => boolean)) => void
  /** Attach to the dropdown container to detect outside clicks. */
  containerRef: React.RefObject<HTMLDivElement | null>
  /**
   * Optional: attach to a portal-rendered menu node. Clicks inside this node
   * are treated as "inside" so they don't dismiss the dropdown (the menu lives
   * in a separate DOM subtree from `containerRef`).
   *
   * Left unattached by callers that render the menu inline (e.g.
   * ReasoningCombobox), in which case it stays `null` and behaviour is
   * identical to checking `containerRef` alone — fully backward-compatible.
   */
  menuRef: React.RefObject<HTMLDivElement | null>
}

/**
 * Shared dropdown open/close state with outside-click dismissal.
 * Extracted from ModelCombobox and ReasoningCombobox.
 */
export function useDropdown(): UseDropdownResult {
  const [isOpen, setIsOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  const handleClickOutside = useCallback((e: MouseEvent) => {
    const target = e.target as Node | null
    if (!target) return
    // Ignore clicks that originate inside the trigger wrapper or the (optional)
    // portal-rendered menu so the dropdown stays open while interacting with it.
    if (containerRef.current?.contains(target)) return
    if (menuRef.current?.contains(target)) return
    setIsOpen(false)
  }, [])

  useEffect(() => {
    if (!isOpen) return
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [isOpen, handleClickOutside])

  return { isOpen, setIsOpen, containerRef, menuRef }
}
