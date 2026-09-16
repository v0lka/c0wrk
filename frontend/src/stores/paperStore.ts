// Paper library store — the single source of truth for the Papers panel.
//
// Owns the loaded library (per project), the id-keyed per-paper record index,
// the pinned card paths, the reader selection, the panel view mode and the RPC
// invoke-state (loading / mutating / error). Backend sync lives in the
// module-level fetchers at the bottom (fetchPaperLibrary / applyPapersChanged)
// so the zustand reducer stays pure and trivially testable.
//
// Selector stability (React #185): every selector returns a PRIMITIVE or a
// DIRECT store reference — never a freshly allocated array/object. Derived
// collections (pinned, filtered) are computed with useMemo in the hooks below,
// outside the selector.

import { useMemo } from 'react'
import { create } from 'zustand'
import {
  getPaper,
  getPapers,
  recordFlashcardReview,
  setPaperPinned,
  type FlashcardGrade,
  type PaperLibrary,
  type PaperRecord,
  type PapersChangedPayload,
} from '@/api/papers'
import { logger } from '@/lib/logger'

export type { PaperLibrary, PaperRecord } from '@/api/papers'

/** Synthetic pseudo-path PREFIX rendered by the file viewer as the paper
 *  workspace/reader for one paper. The paper slug is appended after it, so a
 *  full pseudo-path is `c0wrk:paper:<slug>` (parallels RESEARCH_TAB_PATH
 *  ('c0wrk:research') in researchStore and REVIEW_TAB_PATH ('c0wrk:review') in
 *  reviewStore). The tab is VIRTUAL (never persisted — see the file-viewer
 *  store's partialize) and its content is fetched by the workspace itself. */
export const PAPER_TAB_PREFIX = 'c0wrk:paper:'

/** The pseudo-path for one paper's workspace tab. */
export function paperTabPath(slug: string): string {
  return `${PAPER_TAB_PREFIX}${slug}`
}

/** Panel presentation mode: browsing the library index vs reading the
 *  selected paper. */
export type PaperViewMode = 'library' | 'reader'

// --- State types ---

interface PaperState {
  /** The project the loaded library belongs to (null = nothing loaded). */
  projectId: string | null
  /** The project's effective research root (the library is a subdirectory). */
  researchRoot: string
  /** Absolute library root (`<research-root>/papers`). */
  root: string
  /** The normalized library, in backend order. */
  papers: PaperRecord[]
  /** Per-paper records indexed by paper id (an O(1) lookup for the reader and
   *  the pin toggles). Entries are the SAME objects as in `papers` — a refresh
   *  reuses the previous object whenever a paper's content is unchanged, so
   *  memoized consumers stay referentially stable. */
  records: Record<string, PaperRecord>
  /** Pinned card paths (research-root-relative, forward slashes). */
  pinned: string[]
  /** The paper selected in the reader (null = none). Stored here (not in
   *  component state) so the selection survives panel remounts. A dangling id
   *  (the paper vanished) resolves to null in `useSelectedPaper` and is
   *  dropped by the next library load. */
  selectedPaperId: string | null
  /** Panel presentation mode. Reset to 'library' on a cross-project load. */
  mode: PaperViewMode
  /** True while a library fetch is in flight (initial load + event refresh). */
  isLoading: boolean
  /** True while a pin mutation is in flight. */
  isMutating: boolean
  /** Last RPC error; null when clean. */
  error: string | null
  /** Wall-clock ms of the last successfully applied library. Lets a consumer
   *  (e.g. a status-events watchdog) verify an event-driven refresh landed. */
  lastSyncAt: number
}

interface PaperActions {
  /** Replace the loaded library (stamping the project it belongs to). Applies
   *  INCREMENTALLY: unchanged paper records keep their previous object
   *  identity, the reader selection survives while its paper still exists, and
   *  `mode` is preserved — only a CROSS-PROJECT load resets the project-scoped
   *  state (selection + mode) along with the data. */
  loadLibrary: (library: PaperLibrary) => void
  /** Upsert a single fetched paper record (GetPaper / a pin RPC result) into
   *  the library and the per-paper index without touching the rest. */
  loadPaper: (record: PaperRecord) => void
  /** Select the paper shown in the reader (or clear with null). */
  selectPaper: (id: string | null) => void
  /** Switch the panel presentation mode. */
  setMode: (mode: PaperViewMode) => void
  setLoading: (loading: boolean) => void
  setMutating: (mutating: boolean) => void
  setError: (error: string | null) => void
  /** Clear everything (project switch to No Project, panel teardown). */
  reset: () => void
}

export type PaperStore = PaperState & PaperActions

// --- Initial state (shared by `create` and `reset`) ---

/** Stable empty collections — the initial/reset references, so selectors that
 *  return them never allocate a fresh array/object per read. */
const EMPTY_PAPERS: PaperRecord[] = []
const EMPTY_RECORDS: Record<string, PaperRecord> = {}
const EMPTY_PINNED: string[] = []

const initialState: PaperState = {
  projectId: null,
  researchRoot: '',
  root: '',
  papers: EMPTY_PAPERS,
  records: EMPTY_RECORDS,
  pinned: EMPTY_PINNED,
  selectedPaperId: null,
  mode: 'library',
  isLoading: false,
  isMutating: false,
  error: null,
  lastSyncAt: 0,
}

// --- Pure helpers ---

/** Deep content equality for two normalized records. Both sides come from the
 *  boundary normalizer, which builds the object with a fixed key order, so a
 *  JSON comparison is a reliable deep equality (and the library is small). */
function paperContentEqual(a: PaperRecord, b: PaperRecord): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b)
}

/** The pinned-path list with one record's pin state folded in (used when a
 *  single paper is upserted outside a full library load). */
function pinnedWithRecord(pinned: string[], record: PaperRecord): string[] {
  const present = pinned.includes(record.card_path)
  if (record.pinned && !present) return [...pinned, record.card_path]
  if (!record.pinned && present) return pinned.filter((path) => path !== record.card_path)
  return pinned
}

// --- Store ---

export const usePaperStore = create<PaperStore>((set) => ({
  ...initialState,

  loadLibrary: (library) =>
    set((state) => {
      // A different project's library never inherits the previous project's
      // selection or mode (paper ids/slugs collide across projects, and a
      // reader left open on a foreign paper would render stale content).
      const crossProject = state.projectId !== null && state.projectId !== library.project_id
      const previous = crossProject ? EMPTY_RECORDS : state.records

      // Incremental merge: reuse the previous record object when the content
      // is unchanged so memoized consumers and the per-paper index stay
      // referentially stable across a refresh.
      const records: Record<string, PaperRecord> = {}
      const papers = library.papers.map((record) => {
        const existing = previous[record.id]
        const next =
          existing !== undefined && paperContentEqual(existing, record) ? existing : record
        records[next.id] = next
        return next
      })

      const selected = state.selectedPaperId
      const selectedPaperId =
        !crossProject && selected !== null && records[selected] !== undefined ? selected : null

      return {
        projectId: library.project_id,
        researchRoot: library.research_root,
        root: library.root,
        papers,
        records,
        pinned: library.pinned,
        selectedPaperId,
        mode: crossProject ? 'library' : state.mode,
        isLoading: false,
        error: null,
        lastSyncAt: Date.now(),
      }
    }),

  loadPaper: (record) =>
    set((state) => {
      const known = state.records[record.id] !== undefined
      const papers = known
        ? state.papers.map((paper) => (paper.id === record.id ? record : paper))
        : [...state.papers, record]
      return {
        papers,
        records: { ...state.records, [record.id]: record },
        pinned: pinnedWithRecord(state.pinned, record),
      }
    }),

  selectPaper: (id) => set({ selectedPaperId: id }),

  setMode: (mode) => set({ mode }),

  setLoading: (isLoading) => set({ isLoading }),

  setMutating: (isMutating) => set({ isMutating }),

  setError: (error) => set({ error, isLoading: false, isMutating: false }),

  reset: () => set(initialState),
}))

// --- Selectors (pure; stable references — never allocate in a selector) ---

/** The loaded library, in order. A direct store reference. */
export function selectPapers(state: PaperStore): PaperRecord[] {
  return state.papers
}

/** The pinned card paths. A direct store reference. */
export function selectPinnedPaperPaths(state: PaperStore): string[] {
  return state.pinned
}

/** The project the loaded library belongs to (null when nothing is loaded). */
export function selectPapersProjectId(state: PaperStore): string | null {
  return state.projectId
}

/** The selected paper id (primitive). */
export function selectSelectedPaperId(state: PaperStore): string | null {
  return state.selectedPaperId
}

/** The panel presentation mode (primitive). */
export function selectPaperViewMode(state: PaperStore): PaperViewMode {
  return state.mode
}

/** True while a library fetch is in flight (primitive). */
export function selectPapersLoading(state: PaperStore): boolean {
  return state.isLoading
}

/** True while a pin mutation is in flight (primitive). */
export function selectPapersMutating(state: PaperStore): boolean {
  return state.isMutating
}

/** Last RPC error (primitive / null). */
export function selectPapersError(state: PaperStore): string | null {
  return state.error
}

/** Wall-clock ms of the last successfully applied library (primitive). The
 *  paper workspace subscribes to it as a refresh key, so a library sync (a
 *  `papers:changed` refetch after the study-paper skill appends sections)
 *  rebuilds its artifact sections. */
export function selectPapersSyncAt(state: PaperStore): number {
  return state.lastSyncAt
}

// --- Custom hooks (granular selectors + useMemo for derived collections) ---

/** The loaded library. */
export function usePapers(): PaperRecord[] {
  return usePaperStore(selectPapers)
}

/** The pinned card paths. */
export function usePinnedPaperPaths(): string[] {
  return usePaperStore(selectPinnedPaperPaths)
}

/** True while a library fetch is in flight. */
export function usePapersLoading(): boolean {
  return usePaperStore(selectPapersLoading)
}

/** The last library RPC error (null when clean). */
export function usePapersError(): string | null {
  return usePaperStore(selectPapersError)
}

/** The panel presentation mode. */
export function usePaperViewMode(): PaperViewMode {
  return usePaperStore(selectPaperViewMode)
}

/** The loaded library's effective research root ('' when nothing is loaded).
 *  The paper workspace uses it to locate the research-root-level comparison
 *  artifacts (`<research-root>/comparisons/`). A primitive — stable. */
export function usePapersResearchRoot(): string {
  return usePaperStore((state) => state.researchRoot)
}

/** The paper selected in the reader, or null. Returns a DIRECT reference out
 *  of the per-paper index — no allocation in the selector. */
export function useSelectedPaper(): PaperRecord | null {
  return usePaperStore((state) =>
    state.selectedPaperId === null ? null : state.records[state.selectedPaperId] ?? null,
  )
}

/** The paper with the given slug from the loaded library, or null. Returns a
 *  DIRECT reference out of the library array (or the stable null) — no
 *  allocation in the selector. Used by the paper workspace tab, whose
 *  pseudo-path carries the slug. */
export function usePaperBySlug(slug: string): PaperRecord | null {
  return usePaperStore((state) => state.papers.find((paper) => paper.slug === slug) ?? null)
}

/** Pinned papers in library order. Derived with useMemo OUTSIDE the selector
 *  (the selectors only read two direct references) so React 19 never sees a
 *  freshly allocated array snapshot. Returns the library array itself when
 *  nothing is pinned. */
export function usePinnedPapers(): PaperRecord[] {
  const papers = usePaperStore(selectPapers)
  const pinned = usePaperStore(selectPinnedPaperPaths)
  return useMemo(() => {
    if (pinned.length === 0) return papers
    const pinnedSet = new Set(pinned)
    return papers.filter((paper) => pinnedSet.has(paper.card_path))
  }, [papers, pinned])
}

/** The library filtered by a case-insensitive query over title/slug/venue/
 *  authors. Derived with useMemo outside the selector; an empty query returns
 *  the library array unchanged. */
export function useFilteredPapers(query: string): PaperRecord[] {
  const papers = usePaperStore(selectPapers)
  return useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (needle === '') return papers
    return papers.filter(
      (paper) =>
        paper.title.toLowerCase().includes(needle) ||
        paper.slug.toLowerCase().includes(needle) ||
        paper.venue.toLowerCase().includes(needle) ||
        paper.authors.some((author) => author.toLowerCase().includes(needle)),
    )
  }, [papers, query])
}

// --- Backend sync (module-level; the store reducer stays pure) ---

/** Ticket of the last-STARTED library fetch. A fetch applies its payload only
 *  while it is still the newest one, so a slow fetch for a project the user has
 *  already switched away from can never clobber the newer project's library
 *  (last-write-wins by initiation order — the same convergence rule the
 *  research store documents). */
let latestFetch = 0

/**
 * Fetch and apply the library for a project. Never throws: a failure is
 * recorded in `error` (the panel renders it) and the previous library is kept.
 * Called on project switch and on every `papers:changed` event.
 */
export async function fetchPaperLibrary(projectId: string): Promise<void> {
  const ticket = ++latestFetch
  usePaperStore.getState().setLoading(true)
  try {
    const library = await getPapers(projectId)
    // A superseded fetch (a newer load started while this one was in flight)
    // is dropped wholesale; it must neither apply stale data nor clear the
    // spinner the newest fetch still owns.
    if (ticket !== latestFetch) return
    usePaperStore.getState().loadLibrary(library)
  } catch (err) {
    if (ticket !== latestFetch) return
    usePaperStore
      .getState()
      .setError(err instanceof Error ? err.message : 'Failed to load paper library')
  }
}

/**
 * Refetch and apply ONE paper (GetPaper) without reloading the whole library —
 * the per-paper incremental path (re-reading the card the user is viewing, or
 * a targeted refresh). No-op without a loaded library or for an unknown paper.
 * Never throws; a failure is recorded in `error` and the previous record kept.
 */
export async function refreshPaper(paperId: string): Promise<void> {
  const state = usePaperStore.getState()
  const { projectId } = state
  if (projectId === null || state.records[paperId] === undefined) return
  try {
    const record = await getPaper(projectId, paperId)
    // A project switch while the RPC was in flight must not splice the old
    // project's paper into the new library.
    const after = usePaperStore.getState()
    if (after.projectId !== projectId) return
    after.loadPaper(record)
  } catch (err) {
    const after = usePaperStore.getState()
    if (after.projectId !== projectId) return
    after.setError(err instanceof Error ? err.message : 'Failed to refresh the paper')
  }
}

/**
 * Pin or unpin the paper in the loaded library. The RPC emits no event, so the
 * resolved promise is the refresh signal: the local record + pinned list are
 * updated from the authoritative pin state on success. No-op without a loaded
 * library or an unknown paper.
 */
export async function togglePaperPin(paperId: string, pinned: boolean): Promise<void> {
  const state = usePaperStore.getState()
  const { projectId } = state
  const record = state.records[paperId]
  if (projectId === null || record === undefined) return
  state.setMutating(true)
  try {
    await setPaperPinned(projectId, paperId, pinned)
  } catch (err) {
    usePaperStore
      .getState()
      .setError(err instanceof Error ? err.message : 'Failed to update the paper pin')
    return
  }
  // A project switch (or reset) while the RPC was in flight must not write the
  // old project's pin into the new project's library.
  const after = usePaperStore.getState()
  if (after.projectId !== projectId) return
  after.loadPaper({ ...record, pinned })
  after.setMutating(false)
}

/** Paper ids whose auto-pin RPC is currently in flight. Guards against a rapid
 *  repeat firing a second, redundant pin RPC before the first resolves. */
const pinEnsuresInFlight = new Set<string>()

/**
 * Ensure the paper is pinned — the "Suggest hypotheses from gaps" gesture
 * auto-pins the paper as prior art through the existing pin RPC. IDEMPOTENT: an
 * already-pinned paper (or one whose ensure is in flight) is left untouched with
 * NO RPC, and a resolved pin can never duplicate the card in the pinned list.
 * No-op without a loaded library or for an unknown paper. Never throws.
 */
export async function ensurePaperPinned(paperId: string): Promise<void> {
  const state = usePaperStore.getState()
  const { projectId } = state
  const record = state.records[paperId]
  if (projectId === null || record === undefined) return
  if (record.pinned || pinEnsuresInFlight.has(paperId)) return
  pinEnsuresInFlight.add(paperId)
  try {
    await togglePaperPin(paperId, true)
  } finally {
    pinEnsuresInFlight.delete(paperId)
  }
}

/**
 * Record one flashcard self-grade for a paper (write-back). The backend appends
 * the grade to the paper's flashcards.md deck atomically and advances the card's
 * stage; the component keeps its own optimistic result and the watcher's
 * `papers:changed` refetch re-reads the deck. No-op without a loaded library, for
 * an unknown paper, or for a card-less id. Never throws: a failure is a log
 * warning (the deck stays as-is on the next refetch).
 */
export async function commitFlashcardReview(
  paperId: string,
  cardId: string,
  grade: FlashcardGrade,
): Promise<void> {
  const state = usePaperStore.getState()
  const { projectId } = state
  if (projectId === null || state.records[paperId] === undefined || cardId === '') return
  try {
    await recordFlashcardReview(projectId, paperId, cardId, grade)
  } catch (err) {
    logger.warn('Failed to record the flashcard review:', err)
  }
}

/**
 * Apply a `papers:changed` event. Events for any project other than the loaded
 * one (a late event after a project switch, or another project's library) are
 * ignored; a matching event INVALIDATES the library by refetching it — the
 * full refetch is deliberate, because a single changed path cannot distinguish
 * an edit from a new or DELETED paper. Returns true when the loaded library was
 * invalidated. The caller (an events hook) may debounce rapid bursts.
 */
export async function applyPapersChanged(event: PapersChangedPayload): Promise<boolean> {
  const { projectId } = usePaperStore.getState()
  if (projectId === null || event.project_id !== projectId) return false
  await fetchPaperLibrary(projectId)
  return true
}
