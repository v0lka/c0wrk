// Paper library store — the single source of truth for the Papers panel.
//
// Owns the loaded library (per project), the id-keyed per-paper record index,
// the pinned card paths and the RPC invoke-state (loading / mutating / error).
// Backend sync lives in the module-level fetchers at the bottom
// (fetchPaperLibrary) so the zustand reducer stays pure and trivially testable.
//
// The reader surface is the file-viewer tab: `openPaper`/`PAPER_TAB_PREFIX` →
// `FileViewerContent` → `PaperWorkspace`, which resolves its record with
// `usePaperBySlug` and reads the library/error/research-root through the hooks
// below. `PapersView` is a pure view over the same store.
//
// Selector stability (React #185): every selector returns a PRIMITIVE or a
// DIRECT store reference — never a freshly allocated array/object.

import { create } from 'zustand'
import {
  getPapers,
  recordFlashcardReview,
  setPaperPinned,
  type FlashcardGrade,
  type PaperLibrary,
  type PaperRecord,
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
  /** Per-paper records indexed by paper id (an O(1) lookup for the pin
   *  toggles). Entries are the SAME objects as in `papers` — a refresh reuses
   *  the previous object whenever a paper's content is unchanged, so memoized
   *  consumers stay referentially stable. */
  records: Record<string, PaperRecord>
  /** Pinned card paths (research-root-relative, forward slashes). */
  pinned: string[]
  /** True while a library fetch is in flight (initial load + event refresh). */
  isLoading: boolean
  /** True while a pin mutation is in flight. */
  isMutating: boolean
  /** Last RPC error; null when clean. */
  error: string | null
  /** Wall-clock ms of the last successfully applied library. This is the
   *  refresh key `PaperWorkspace` passes to `usePaperArtifacts`/
   *  `useComparisons`/`usePaperLiterature`, so a `papers:changed` sync rebuilds
   *  the paper's artifact sections after the study-paper skill appends to a
   *  card. There is NO convergence watchdog consumer: the library is refreshed
   *  only on a project switch and on a RECEIVED `papers:changed` (see
   *  `usePapersEvents`), so a dropped/coalesced watcher event is not recovered
   *  until the next event or switch. */
  lastSyncAt: number
}

interface PaperActions {
  /** Replace the loaded library (stamping the project it belongs to). Applies
   *  INCREMENTALLY: unchanged paper records keep their previous object
   *  identity. A CROSS-PROJECT load also clears the invoke-state (a stuck
   *  `isMutating` from the departed project must not survive). */
  loadLibrary: (library: PaperLibrary) => void
  /** Upsert a single fetched paper record into the library and the per-paper
   *  index without touching the rest. */
  loadPaper: (record: PaperRecord) => void
  setLoading: (loading: boolean) => void
  setMutating: (mutating: boolean) => void
  setError: (error: string | null) => void
  /** Clear everything (project switch to No Project, panel teardown) AND
   *  invalidate any in-flight library fetch so it cannot repopulate the store
   *  it just cleared. */
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
      // A different project's library must not inherit the previous project's
      // per-paper index (paper ids/slugs collide across projects).
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

      return {
        projectId: library.project_id,
        researchRoot: library.research_root,
        root: library.root,
        papers,
        records,
        pinned: library.pinned,
        isLoading: false,
        // A project-scoped load always clears the mutation flag: a pin that was
        // in flight for the departed project must not leave the new project's
        // panel stuck "mutating".
        isMutating: false,
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

  setLoading: (isLoading) => set({ isLoading }),

  setMutating: (isMutating) => set({ isMutating }),

  setError: (error) => set({ error, isLoading: false, isMutating: false }),

  reset: () => {
    // Invalidate every in-flight library fetch (bumping the ticket makes them
    // stale) BEFORE clearing, so a slow fetch for the departed project cannot
    // resolve later and repopulate the store that was just cleared.
    invalidatePendingFetches()
    set(initialState)
  },
}))

// --- Selectors (pure; stable references — never allocate in a selector) ---

/** The loaded library, in order. A direct store reference. */
export function selectPapers(state: PaperStore): PaperRecord[] {
  return state.papers
}

/** The project the loaded library belongs to (null when nothing is loaded). */
export function selectPapersProjectId(state: PaperStore): string | null {
  return state.projectId
}

/** True while a library fetch is in flight (primitive). */
export function selectPapersLoading(state: PaperStore): boolean {
  return state.isLoading
}

/** True while a pin mutation is in flight (primitive). The pin control can use
 *  it to disable itself while a mutation is in flight. */
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

// --- Custom hooks (granular selectors) ---

/** The loaded library. */
export function usePapers(): PaperRecord[] {
  return usePaperStore(selectPapers)
}

/** True while a library fetch is in flight. */
export function usePapersLoading(): boolean {
  return usePaperStore(selectPapersLoading)
}

/** The last library RPC error (null when clean). */
export function usePapersError(): string | null {
  return usePaperStore(selectPapersError)
}

/** The loaded library's effective research root ('' when nothing is loaded).
 *  The paper workspace uses it to locate the research-root-level comparison
 *  artifacts (`<research-root>/comparisons/`). A primitive — stable. */
export function usePapersResearchRoot(): string {
  return usePaperStore((state) => state.researchRoot)
}

/** The paper with the given slug from the loaded library, or null. Returns a
 *  DIRECT reference out of the library array (or the stable null) — no
 *  allocation in the selector. Used by the paper workspace tab, whose
 *  pseudo-path carries the slug. */
export function usePaperBySlug(slug: string): PaperRecord | null {
  return usePaperStore((state) => state.papers.find((paper) => paper.slug === slug) ?? null)
}

// --- Backend sync (module-level; the store reducer stays pure) ---

/** Ticket of the last-STARTED library fetch. A fetch applies its payload only
 *  while it is still the newest one, so a slow fetch for a project the user has
 *  already switched away from — or one still in flight when the panel was reset
 *  — can never clobber the newer state (last-write-wins by initiation order). */
let latestFetch = 0

/** Invalidate every in-flight library fetch. Bumping the ticket makes any fetch
 *  started before this call stale, so `reset()` can drop a fetch that would
 *  otherwise repopulate the cleared store. */
export function invalidatePendingFetches(): void {
  latestFetch += 1
}

/**
 * Fetch and apply the library for a project. Never throws. Returns `true` when
 * the fetched payload was actually APPLIED, `false` when it failed or was
 * superseded by a newer fetch / a `reset()` — so a caller can tell an applied
 * invalidation from a dropped one. On failure the previous library is kept and
 * the error is recorded (the panel renders it).
 */
export async function fetchPaperLibrary(projectId: string): Promise<boolean> {
  const ticket = ++latestFetch
  usePaperStore.getState().setLoading(true)
  try {
    const library = await getPapers(projectId)
    // A superseded fetch (a newer load started, or the store was reset, while
    // this one was in flight) is dropped wholesale; it must neither apply stale
    // data nor clear the spinner the newest fetch still owns.
    if (ticket !== latestFetch) return false
    usePaperStore.getState().loadLibrary(library)
    return true
  } catch (err) {
    if (ticket !== latestFetch) return false
    usePaperStore
      .getState()
      .setError(err instanceof Error ? err.message : 'Failed to load paper library')
    return false
  }
}

/** Paper ids whose pin RPC is currently in flight. Guards against a rapid
 *  repeat (a double-click, or a duplicate dispatch) firing a second, redundant
 *  pin RPC before the first resolves. */
const pinTogglesInFlight = new Set<string>()

/**
 * Pin or unpin the paper in the loaded library. The RPC emits no event, so the
 * resolved promise is the refresh signal: the local record + pinned list are
 * updated from the authoritative pin state on success. No-op without a loaded
 * library or an unknown paper, and while an identical toggle is already in
 * flight (a re-entry guard, symmetric with `ensurePaperPinned`).
 */
export async function togglePaperPin(paperId: string, pinned: boolean): Promise<void> {
  const state = usePaperStore.getState()
  const { projectId } = state
  if (projectId === null || state.records[paperId] === undefined) return
  if (pinTogglesInFlight.has(paperId)) return
  pinTogglesInFlight.add(paperId)
  state.setMutating(true)
  try {
    await setPaperPinned(projectId, paperId, pinned)
    // A project switch (or reset) while the RPC was in flight must not write the
    // old project's pin into the new project's library.
    const after = usePaperStore.getState()
    if (after.projectId !== projectId) return
    // Re-read the record AFTER the await so a card that a concurrent
    // `papers:changed` refetch refreshed in the meantime is not reverted by the
    // pre-await snapshot (only the `pinned` flag is folded in).
    const current = after.records[paperId] ?? state.records[paperId]
    if (current !== undefined) after.loadPaper({ ...current, pinned })
  } catch (err) {
    // Only the project that issued the RPC may receive its failure — a switch
    // while the write was in flight must not surface the old project's error in
    // the new project's panel.
    const after = usePaperStore.getState()
    if (after.projectId !== projectId) return
    after.setError(err instanceof Error ? err.message : 'Failed to update the paper pin')
  } finally {
    pinTogglesInFlight.delete(paperId)
    // Always clear the mutation flag — even on the early returns above — so a
    // project switch mid-flight cannot leave the panel stuck "mutating".
    usePaperStore.getState().setMutating(false)
  }
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
 * an unknown paper, or for a card-less id. Never throws: a failure is reported
 * on the store's error line (rendered by the workspace / panel) so the user is
 * not shown an optimistic grade that silently reverts on the next refetch.
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
    // Only write the failure into the store if the same project is still loaded
    // (a switch while the grade was in flight must not surface it elsewhere).
    const after = usePaperStore.getState()
    if (after.projectId !== projectId) return
    after.setError(
      err instanceof Error ? err.message : 'Failed to record the flashcard review',
    )
  }
}
