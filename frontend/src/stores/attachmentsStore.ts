// Zustand store for a session's pending file attachments.
//
// Stable selectors: the attachments array is a direct store property returned
// by reference — never allocated inside a useStore selector. React 19's
// useSyncExternalStore compares snapshots by reference; a fresh array on every
// render would trigger an infinite re-render loop (error #185). Derive derived
// values (length, etc.) via the useAttachments() / useHasAttachments() hooks
// which return primitives or direct references.

import { create } from 'zustand'
import type { AttachmentInfoUI } from '@/types/models'

interface AttachmentsState {
  /** Pending attachments for the active session. */
  attachments: AttachmentInfoUI[]
  /**
   * Accumulated attachment-id → original-name map. Populated from every
   * attachment list the store receives. The send-flush is a no-op for this
   * cache since it carries an empty list. Entries are never removed
   * individually so that committed attachments stay resolvable after the
   * pending chips are cleared — the read_attachment tool card uses this to
   * show the file name instead of the opaque attachment id. Cleared on
   * session switch together with the pending list.
   */
  namesById: Record<string, string>
}

interface AttachmentsActions {
  /** Replace the entire pending list (the backend always sends the full list). */
  setAttachments: (attachments: AttachmentInfoUI[]) => void
  /** Clear the store (e.g. on session switch). */
  clear: () => void
}

export const useAttachmentsStore = create<AttachmentsState & AttachmentsActions>((set) => ({
  attachments: [],
  namesById: {},

  setAttachments: (attachments) =>
    set((state) => {
      // Fold the freshly received attachment metadata into the id→name cache.
      // Reusing the incoming list keeps the `attachments` reference stable for
      // the chips selector; only namesById gets a new object.
      let namesById = state.namesById
      for (const a of attachments) {
        if (namesById[a.id] !== a.originalName) {
          if (namesById === state.namesById) namesById = { ...state.namesById }
          namesById[a.id] = a.originalName
        }
      }
      return { attachments, namesById }
    }),

  clear: () => set({ attachments: [], namesById: {} }),
}))

/**
 * Read the active session's pending attachments.
 *
 * Returns the direct store array reference — referentially stable as long as
 * the list itself is unchanged, so this is safe to pass to useStore directly.
 */
export function useAttachments(): AttachmentInfoUI[] {
  return useAttachmentsStore((s) => s.attachments)
}

/** Whether there are any pending attachments (primitive — always stable). */
export function useHasAttachments(): boolean {
  return useAttachmentsStore((s) => s.attachments.length > 0)
}

/**
 * Resolve an attachment id to its original file name. Returns undefined when
 * the id is unknown (e.g. an attachment committed before this session's store
 * was seeded, such as after an app restart). A primitive selector, so it only
 * re-renders when the resolved name actually changes.
 */
export function useAttachmentName(id: string | undefined | null): string | undefined {
  return useAttachmentsStore((s) => (id ? s.namesById[id] : undefined))
}
