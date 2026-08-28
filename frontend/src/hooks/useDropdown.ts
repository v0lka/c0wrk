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
 *
 * `disabled` closes an open menu the moment it flips to true: portal-rendered
 * menus attach to document.body, OUTSIDE any pointer-events-none lock wrapper
 * around the trigger, so without this an open menu would stay clickable while
 * its owner is locked (e.g. the chat toolbar mid-task). Inline menus benefit
 * too — keyboard focus would otherwise keep them interactive.
 */
export function useDropdown(disabled = false): UseDropdownResult {
  const [isOpen, setIsOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (disabled) setIsOpen(false)
  }, [disabled])

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
