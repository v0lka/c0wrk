// Optimistic attachment-upload lifecycle shared by every staging entry point
// (📎 picker + drag-and-drop via useStageAttachments, clipboard paste via
// usePasteHandler) and by the `attachments:changed` event handler.
//
// The backend's AttachFiles/PasteFromClipboard convert files (markitdown /
// image re-encode) server-side with no mid-flight cancellation, and they emit
// `attachments:changed` INCREMENTALLY after each conversion — always carrying
// the FULL pending list. The backend offers no request↔attachment
// correlation: image records swap the source path for the processed copy's
// path and clipboard images are renamed server-side, so string identity
// (name/path) alone cannot reliably join a placeholder to its landed record.
// This module therefore anchors every join on ATTACHMENT IDS:
//
//   begin → each batch snapshots the session's staged attachment ids (its ID
//           window); placeholders (spinner chips) appear in the store instantly;
//   claim → on every incoming full list, ids OUTSIDE an upload's window are
//           attributed to exactly one in-flight upload (exact path > basename
//           > FIFO), retiring placeholders one-by-one as conversions land;
//   complete/fail → the batch's placeholders drain as the staging RPC settles;
//   cancel → the placeholder disappears at once; the upload keeps claiming
//            inside its window, and every id it claims is removed from the
//            backend via RemoveAttachment and filtered out of subsequent
//            event/RPC lists until the removal settles.
//
// The ID window is what keeps unrelated records safe: a same-basename file
// staged BEFORE the batch and a re-added file staging AFTER a cancel both
// fall outside it, so a cancel can only ever delete its own upload's record.
// Name/path comparison survives only as an attribution HINT inside the window
// (which of several concurrently converting files just landed), never as the
// authority.

import { isImagePath, removeAttachment } from '@/api/attachments'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import type { AttachmentInfoUI, AttachmentUploadUI } from '@/types/models'
import { logger } from '@/lib/logger'

/** Descriptor of one file about to be staged. */
export interface UploadDescriptor {
  /** Absolute path ('' when unknown — clipboard image pastes). */
  path: string
  /** Display label for the spinner chip (basename, or the generic paste
   *  label). Doubles as an attribution hint inside the upload's ID window. */
  fileName: string
  isImage: boolean
}

/** Registry entry for an in-flight optimistic upload. */
interface UploadEntry {
  /** Placeholder id — doubles as the registry key and the cancel key. */
  id: string
  sessionId: string
  path: string
  fileName: string
  /** Staged attachment ids captured at begin — the entry may only claim ids
   *  OUTSIDE this set (records that did not exist when it started). */
  baseline: ReadonlySet<string>
  /** The landed attachment id claimed by this upload (one file → at most one
   *  record). Null while conversion is still running or when it failed. */
  claimedId: string | null
  /** Set when the user cancelled this upload via its spinner chip's X. */
  cancelled: boolean
  /** Set when the batch's staging RPC settled (complete/fail). */
  settled: boolean
  /** RemoveAttachment RPCs in flight for the claimed id. */
  pendingRemovals: number
}

/** In-flight optimistic uploads keyed by placeholder id. Map iteration order
 *  is begin order — the FIFO attribution fallback hands ids to the OLDEST
 *  unclaimed entry first, mirroring the backend's sequential in-order
 *  conversion (AttachFiles processes paths strictly one after another). */
const activeUploads = new Map<string, UploadEntry>()

/** `${sessionId}:${attachmentId}` keys of RemoveAttachment RPCs already sent,
 *  so repeated event sightings of a cancelled attachment fire exactly one
 *  removal. Entries self-clean when the RPC settles. */
const removalsSent = new Set<string>()

/** Label for an optimistic clipboard-image placeholder. Display-only: the
 *  backend normalizes every platform's clipboard image to PNG under its own
 *  "pasted-image.png" name, so guessing the extension from the webview's
 *  MIME type would only mislabel — claiming is ID-window based, not name
 *  based, so the label never needs to match the backend's displayName. */
const PASTED_IMAGE_LABEL = 'pasted-image'

/** Delete a cancelled entry once its batch settled and no removal RPC is in
 *  flight — until then it keeps filtering its claimed id out of event/RPC
 *  lists so interim `attachments:changed` events cannot flash the cancelled
 *  file back as a real chip. */
function retireEntryIfDone(entry: UploadEntry): void {
  if (entry.cancelled && entry.settled && entry.pendingRemovals === 0) {
    activeUploads.delete(entry.id)
  }
}

/** Fire a deduped RemoveAttachment RPC for a cancelled upload's claimed id.
 *  Failures are logged, never thrown — the backend's `attachments:changed`
 *  event remains the reconciler. */
function scheduleRemoval(sessionId: string, entry: UploadEntry, attachmentId: string): void {
  const key = `${sessionId}:${attachmentId}`
  if (removalsSent.has(key)) return
  removalsSent.add(key)
  entry.pendingRemovals++
  removeAttachment(sessionId, attachmentId)
    .catch((err) => {
      logger.error('Failed to remove cancelled attachment:', err)
    })
    .finally(() => {
      removalsSent.delete(key)
      entry.pendingRemovals--
      retireEntryIfDone(entry)
    })
}

/**
 * Attribute every not-yet-claimed attachment id that lies OUTSIDE each
 * upload's begin-time window to exactly one in-flight upload of the session.
 *
 * Preference inside the window: exact path (when both sides know one) >
 * basename > FIFO (oldest unclaimed entry). Returns the placeholder ids of
 * NON-cancelled uploads that just claimed (their spinner chips retire). A
 * CANCELLED upload claims silently — its claimed id is scheduled for backend
 * removal right here.
 */
function claimLandedAttachments(sessionId: string, list: readonly AttachmentInfoUI[]): string[] {
  const open: UploadEntry[] = []
  for (const entry of activeUploads.values()) {
    if (entry.sessionId === sessionId && entry.claimedId === null) open.push(entry)
  }
  if (open.length === 0) return []

  const retired: string[] = []
  for (const att of list) {
    // The window check is the authority: an id present in an entry's baseline
    // existed before that upload began and can never be its record.
    const eligible = open.filter((e) => e.claimedId === null && !e.baseline.has(att.id))
    if (eligible.length === 0) continue
    const byPath = eligible.find(
      (e) => e.path !== '' && att.path !== undefined && att.path === e.path,
    )
    const byName = eligible.find((e) => e.fileName === att.originalName)
    const fifo = eligible[0]
    const entry = byPath ?? byName ?? fifo
    if (!entry) continue
    entry.claimedId = att.id
    if (entry.cancelled) scheduleRemoval(sessionId, entry, att.id)
    else retired.push(entry.id)
  }
  return retired
}

/** Drop an unclaimed cancelled entry that matches a re-added descriptor —
 *  the user cancelled this very file and attached it again before the batch
 *  settled, so the stale cancel must not claim (and remove) the fresh
 *  upload's record. Entries that already claimed keep running: they only
 *  filter their OWN claimed id, which the re-add never shares. */
function purgeStaleCancel(sessionId: string, d: UploadDescriptor): void {
  for (const entry of activeUploads.values()) {
    if (entry.sessionId !== sessionId || !entry.cancelled || entry.claimedId !== null) continue
    const samePath = d.path !== '' && entry.path === d.path
    if (samePath || entry.fileName === d.fileName) activeUploads.delete(entry.id)
  }
}

/** Stage placeholders for the given descriptors and register their lifecycle.
 *  Returns the created placeholder records (for the caller's completion call).
 *
 *  The session's currently staged attachment ids are snapshotted as the
 *  batch's ID window: the batch may only claim records appearing LATER. */
export function beginAttachmentUploads(
  sessionId: string,
  descriptors: UploadDescriptor[],
): AttachmentUploadUI[] {
  for (const d of descriptors) purgeStaleCancel(sessionId, d)
  const baseline = new Set(
    (useAttachmentsStore.getState().attachmentsBySession[sessionId] ?? []).map((a) => a.id),
  )
  const uploads: AttachmentUploadUI[] = descriptors.map((d) => ({
    id: crypto.randomUUID(),
    fileName: d.fileName,
    path: d.path,
    isImage: d.isImage,
  }))
  for (const u of uploads) {
    activeUploads.set(u.id, {
      id: u.id,
      sessionId,
      path: u.path,
      fileName: u.fileName,
      baseline,
      claimedId: null,
      cancelled: false,
      settled: false,
      pendingRemovals: 0,
    })
  }
  useAttachmentsStore.getState().beginUploads(sessionId, uploads)
  return uploads
}

/** Cancel one in-flight upload: drop its spinner chip immediately and let the
 *  entry keep claiming inside its window — whatever record it claims (right
 *  now, or when a racing conversion lands later) is removed on the backend.
 *  Covers both cancel-before-convert and cancel-after-convert. */
export function cancelAttachmentUpload(sessionId: string, upload: AttachmentUploadUI): void {
  const entry = activeUploads.get(upload.id)
  if (entry) entry.cancelled = true
  useAttachmentsStore.getState().endUploads(sessionId, [upload.id])
  if (!entry) return
  // Sweep the currently staged list right away — the cancelled file may have
  // finished converting (its real chip landed via an earlier event) while the
  // placeholder was still up. The window check keeps pre-existing records of
  // other uploads (even same-basename ones) out of the sweep.
  const staged = useAttachmentsStore.getState().attachmentsBySession[sessionId] ?? []
  claimLandedAttachments(sessionId, staged)
}

/**
 * Apply an incoming FULL pending list (`attachments:changed` event or initial
 * fetch) to the upload registry: attribute newly landed ids to in-flight
 * uploads (retiring their placeholders one-by-one as conversions finish),
 * then strip the ids claimed by CANCELLED uploads and schedule their backend
 * removal. Returns the list the caller should write into the store.
 */
export function processIncomingAttachments(
  sessionId: string,
  list: AttachmentInfoUI[],
): AttachmentInfoUI[] {
  const retired = claimLandedAttachments(sessionId, list)
  if (retired.length > 0) useAttachmentsStore.getState().endUploads(sessionId, retired)

  // Cancelled uploads suppress every sighting of their claimed id until the
  // removal settles (no flash-back from interim events).
  const cancelledClaims = new Map<string, UploadEntry>()
  for (const entry of activeUploads.values()) {
    if (entry.sessionId === sessionId && entry.cancelled && entry.claimedId !== null) {
      cancelledClaims.set(entry.claimedId, entry)
    }
  }
  if (cancelledClaims.size === 0) return list
  return list.filter((att) => {
    const entry = cancelledClaims.get(att.id)
    if (!entry) return true
    scheduleRemoval(sessionId, entry, att.id)
    return false
  })
}

/**
 * Complete an upload batch with the staging RPC's FULL pending list.
 *
 * Runs one final claim pass (the RPC result is authoritative for anything the
 * incremental events missed), drains the batch's placeholders, and returns
 * the list the caller should write into the store — ids claimed by cancelled
 * uploads stripped and removed on the backend. Cancelled entries stay
 * registered until their removal RPCs settle so interim events keep
 * filtering the claimed ids.
 */
export function completeAttachmentUploads(
  sessionId: string,
  uploads: AttachmentUploadUI[],
  incoming: AttachmentInfoUI[],
): AttachmentInfoUI[] {
  const kept = processIncomingAttachments(sessionId, incoming)
  useAttachmentsStore.getState().endUploads(sessionId, uploads.map((u) => u.id))
  for (const u of uploads) {
    const entry = activeUploads.get(u.id)
    if (!entry) continue
    entry.settled = true
    if (!entry.cancelled) activeUploads.delete(u.id)
    else retireEntryIfDone(entry)
  }
  return kept
}

/** Drain a failed upload batch: placeholders disappear, entries unregister. */
export function failAttachmentUploads(uploads: AttachmentUploadUI[]): void {
  for (const u of uploads) {
    const entry = activeUploads.get(u.id)
    activeUploads.delete(u.id)
    if (entry) useAttachmentsStore.getState().endUploads(entry.sessionId, [u.id])
  }
}

/**
 * Build optimistic upload descriptors for a non-fast-path paste, mirroring
 * the backend's image → files precedence and vision gating so a placeholder
 * exists for exactly the files the backend will actually stage:
 *
 *  - an image item with vision support → ONE descriptor labelled
 *    "pasted-image" (display-only — the backend normalizes clipboard images
 *    to PNG under its own name, so no extension is synthesized here);
 *  - otherwise every file item whose name isn't a vision-gated image ext.
 *
 * MUST run synchronously in the paste event (DataTransferItem.getAsFile is
 * only valid before the first await).
 */
export function collectPasteUploadDescriptors(
  data: DataTransfer,
  supportsVision: boolean,
): UploadDescriptor[] {
  const fileNames: string[] = []
  let sawImage = false
  const items = data.items ?? []
  for (let i = 0; i < items.length; i++) {
    const item = items[i]
    if (!item || item.kind !== 'file') continue
    if ((item.type ?? '').startsWith('image/')) {
      // Image content wins the backend's precedence — remember that an image
      // is present and drop file items entirely.
      sawImage = true
      continue
    }
    const name = item.getAsFile()?.name ?? 'file'
    fileNames.push(name)
  }

  if (sawImage) {
    return supportsVision
      ? [{ path: '', fileName: PASTED_IMAGE_LABEL, isImage: true }]
      : [] // image paste rejected by a non-vision model — nothing stages
  }
  return fileNames
    .filter((name) => supportsVision || !isImagePath(name))
    .map((name) => ({ path: '', fileName: name, isImage: isImagePath(name) }))
}

/** Test/teardown helper: forget all in-flight state (registry + dedupe set). */
export function resetAttachmentUploadState(): void {
  activeUploads.clear()
  removalsSent.clear()
}
