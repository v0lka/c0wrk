import { useSyncExternalStore } from 'react'

/**
 * The single, programmatic source of truth for "only one bookmark icon and only
 * one collapse chevron visible at a time" across the whole chat.
 *
 * Every hover over the chat computes the *nearest* block under the pointer and
 * writes BOTH targets here in one atomic {@link set} call — so showing a new
 * block automatically hides whatever was shown before. Moving the pointer off
 * the chat clears both via {@link clear}.
 *
 * There is deliberately NO CSS `:hover` / `group-hover` involvement.
 * Visibility is driven purely by this store: each star/chevron reads the field
 * it cares about and hides itself (the chevron via an inline `visibility`
 * style; the star via `opacity-0 pointer-events-none` classes so it stays
 * keyboard-focusable — see BookmarkStar).
 * That is the fix for the "stuck after a fast mouse-out" bug — nothing is left
 * to the browser's hover cascade, and a hidden icon is never a live hit-target.
 */
export interface ChatHoverTargets {
  /**
   * The `data-bookmark-id` of the row currently under the pointer, or `null`
   * when the pointer is over a non-row (or has left the chat).
   */
  bookmark: string | null
  /**
   * The `data-chevron-reveal-id` of the collapsible currently under the
   * pointer, or `null` when there is none (or the pointer has left the chat).
   */
  chevron: string | null
}

let targets: ChatHoverTargets = { bookmark: null, chevron: null }
const listeners = new Set<() => void>()

function emit(): void {
  for (const listener of listeners) listener()
}

export const chatHoverStore = {
  getSnapshot(): ChatHoverTargets {
    return targets
  },
  subscribe(listener: () => void): () => void {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  },
  /**
   * Atomically replace both targets. Setting the block under the pointer here
   * automatically hides whatever was shown previously, because the store holds
   * exactly one bookmark and one chevron at any time.
   */
  set(next: ChatHoverTargets): void {
    if (next.bookmark === targets.bookmark && next.chevron === targets.chevron) {
      return
    }
    targets = next
    emit()
  },
  /** Hide both the previously-shown bookmark and chevron (pointer left chat). */
  clear(): void {
    if (targets.bookmark === null && targets.chevron === null) {
      return
    }
    targets = { bookmark: null, chevron: null }
    emit()
  },
}

/**
 * Subscribes to ONLY the active bookmark id as a primitive.
 *
 * Each BookmarkStar needs just one field. Returning the primitive from the
 * getSnapshot means React's `Object.is` comparison bails out of re-rendering
 * every subscriber whose bookmark value is unchanged — so a pointer transition
 * that lights a new chevron without changing the active bookmark, or vice
 * versa, re-renders only the components that actually changed (previously the
 * whole `{bookmark, chevron}` object was a fresh reference on every store
 * write, forcing a re-render of every star and every collapsible on each
 * boundary crossing).
 */
export function useChatHoverBookmark(): string | null {
  return useSyncExternalStore(
    chatHoverStore.subscribe,
    () => chatHoverStore.getSnapshot().bookmark,
    () => chatHoverStore.getSnapshot().bookmark,
  )
}

/** Subscribes to ONLY the active chevron reveal id as a primitive. */
export function useChatHoverChevron(): string | null {
  return useSyncExternalStore(
    chatHoverStore.subscribe,
    () => chatHoverStore.getSnapshot().chevron,
    () => chatHoverStore.getSnapshot().chevron,
  )
}
