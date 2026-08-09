import { useEffect, useCallback, useRef } from 'react'
import { MessageSquarePlus, Telescope } from 'lucide-react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useUIStore } from '@/stores/uiStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { cn } from '@/lib/utils'

interface FileViewerContextMenuProps {
  /** File reference string to insert, e.g. `@path/to/file.go#L10-25`. */
  reference: string
  /** Selected source text to seed a "Find similar" vector search. */
  selectedText: string
  /** Viewport coordinates where the menu should appear. */
  position: { x: number; y: number } | null
  /** Called when the menu should close. */
  onClose: () => void
}

/**
 * A contextual dropdown rendered at the pointer position offering "Add to chat"
 * and "Find similar" (vector search seeded from the selection).
 *
 * Handles its own dismissal on click outside, Escape key, and window scroll.
 * The parent controls visibility via the `position` prop — when `null`,
 * nothing is rendered.
 */
export function FileViewerContextMenu({ reference, selectedText, position, onClose }: FileViewerContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  const insertTextIntoInput = useInputModeStore((s) => s.insertTextIntoInput)

  const handleAddToChat = useCallback(() => {
    insertTextIntoInput(reference)
    onClose()
  }, [insertTextIntoInput, reference, onClose])

  // Seed the vector-search query with the selected code and switch to the
  // Semantics panel so the user sees the results.
  //
  // IMPORTANT: the actual search is NOT triggered by setQuery directly. It is
  // driven by an auto-search effect inside useVectorSearch, which lives in
  // VectorStorePanel. That panel is always mounted — WorkspacePanel renders it
  // unconditionally inside its TabsContent (no forceMount=false / lazy unmount),
  // so the effect is guaranteed to be subscribed and will fire when the index
  // reports ready (incl. the vector_index:status → ready subscription). This
  // handler therefore RELIES on VectorStorePanel staying eagerly mounted. If the
  // Semantics tab ever becomes lazy-unmounted, setQuery alone would update the
  // store with no subscriber to run the search — make setQuery trigger search
  // itself (or keep the panel mounted) before flipping that mount policy.
  const handleFindSimilar = useCallback(() => {
    const text = selectedText.trim()
    if (!text) {
      onClose()
      return
    }
    useVectorIndexStore.getState().setQuery(text)
    useUIStore.getState().setWorkspaceTab('semantics')
    onClose()
  }, [selectedText, onClose])

  // Dismiss on click outside
  useEffect(() => {
    if (!position) return
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose()
      }
    }
    document.addEventListener('mousedown', handler, true)
    return () => document.removeEventListener('mousedown', handler, true)
  }, [position, onClose])

  // Dismiss on Escape
  useEffect(() => {
    if (!position) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [position, onClose])

  // Dismiss on scroll (capture phase to catch all scrolling)
  useEffect(() => {
    if (!position) return
    window.addEventListener('scroll', onClose, true)
    return () => window.removeEventListener('scroll', onClose, true)
  }, [position, onClose])

  if (!position) return null

  return (
    <div
      ref={menuRef}
      role="menu"
      aria-label="File viewer context menu"
      style={{
        position: 'fixed',
        left: position.x,
        top: position.y,
        zIndex: 9999,
      }}
      className={cn(
        'min-w-[10rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
        'animate-in fade-in-0 zoom-in-95',
      )}
    >
      <button
        role="menuitem"
        onClick={handleAddToChat}
        className={cn(
          'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
          'hover:bg-muted/50 focus:bg-muted/50',
          '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
        )}
      >
        <MessageSquarePlus className="size-4" />
        Add to chat
      </button>
      <div className="my-1 h-px bg-border" />
      <button
        role="menuitem"
        onClick={handleFindSimilar}
        className={cn(
          'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
          'hover:bg-muted/50 focus:bg-muted/50',
          '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
        )}
      >
        <Telescope className="size-4" />
        Find similar
      </button>
    </div>
  )
}
