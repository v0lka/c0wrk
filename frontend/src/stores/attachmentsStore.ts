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
}

interface AttachmentsActions {
  /** Replace the entire pending list (the backend always sends the full list). */
  setAttachments: (attachments: AttachmentInfoUI[]) => void
  /** Clear the store (e.g. on session switch). */
  clear: () => void
}

export const useAttachmentsStore = create<AttachmentsState & AttachmentsActions>((set) => ({
  attachments: [],

  setAttachments: (attachments) => set({ attachments }),

  clear: () => set({ attachments: [] }),
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
