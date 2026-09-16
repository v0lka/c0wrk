// Papers view — the literature ("papers") showcase inside the Research panel.
//
// A pure view over paperStore (the library sync — project-switch fetch +
// `papers:changed` — lives in usePapersEvents, mounted once at the App root),
// plus the invocation surface: a "Study paper" field and a study-mode selector
// that dispatch the `study-paper` skill through the message sender (mirroring
// ResearchQuickActions, Shift = a fresh session). Each row lists one studied
// paper with its mode/reading/verdict/confidence badges and row actions: open the
// reader workspace as a viewer tab, go deeper (one mode deeper, append-only),
// compare, pin/unpin, and suggest hypotheses from the paper's gaps (which
// auto-pins the paper as prior art).

import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  FileText,
  Microscope,
  GitCompare,
  Pin,
  PinOff,
  Lightbulb,
} from 'lucide-react'
import { useMessageSender } from '@/hooks/useMessageSender'
import {
  usePaperStore,
  usePapers,
  usePapersError,
  usePapersLoading,
  usePapersResearchRoot,
  togglePaperPin,
  ensurePaperPinned,
} from '@/stores/paperStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { cn } from '@/lib/utils'
import type { PaperRecord } from '@/api/papers'
import {
  STUDY_MODE_OPTIONS,
  STUDY_PAPER_SKILL,
  RESEARCH_HYPOTHESIS_SKILL,
  buildStudyPrompt,
  buildDeepenPrompt,
  buildComparePrompt,
  buildCompareSelectedPrompt,
  buildProposeHypothesisPrompt,
  type StudyMode,
} from './paperActions'

const INPUT_CLASS =
  'min-w-0 flex-1 rounded border border-border bg-background px-1.5 py-0.5 text-[11px] text-foreground placeholder:text-muted-foreground focus:outline-none'

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50'

const BADGE_CLASS =
  'inline-flex items-center rounded px-1 py-0 text-[10px] font-medium uppercase tracking-wide'

/** Background/foreground token pairs per semantic tone (design tokens only). */
const TONE_CLASS: Record<string, string> = {
  neutral: 'bg-muted text-muted-foreground',
  info: 'bg-info/15 text-info',
  success: 'bg-success/15 text-success',
  warning: 'bg-warning/15 text-warning',
  destructive: 'bg-destructive/15 text-destructive',
}

function toneForVerdict(verdict: PaperRecord['verdict']): string {
  switch (verdict) {
    case 'accepted':
      return 'success'
    case 'rejected':
      return 'destructive'
    case 'uncertain':
      return 'warning'
    default:
      return 'neutral'
  }
}

/** The mode/reading/verdict/confidence badges for one paper (empty values omitted). */
function PaperBadges({ paper }: { paper: PaperRecord }) {
  return (
    <div className="flex flex-wrap items-center gap-1">
      {paper.mode !== '' && (
        <span data-testid="paper-badge-mode" className={cn(BADGE_CLASS, TONE_CLASS.info)}>
          {paper.mode}
        </span>
      )}
      {paper.reading !== '' && (
        <span data-testid="paper-badge-reading" className={cn(BADGE_CLASS, TONE_CLASS.neutral)}>
          {paper.reading}
        </span>
      )}
      {paper.verdict !== '' && (
        <span
          data-testid="paper-badge-verdict"
          className={cn(BADGE_CLASS, TONE_CLASS[toneForVerdict(paper.verdict)])}
        >
          {paper.verdict}
        </span>
      )}
      {paper.confidence !== '' && (
        <span data-testid="paper-badge-confidence" className={cn(BADGE_CLASS, TONE_CLASS.neutral)}>
          {paper.confidence}
        </span>
      )}
    </div>
  )
}

/** The one-line metadata under a row's title (id · year · venue). */
function metaLine(paper: PaperRecord): string {
  return [paper.id, paper.year > 0 ? String(paper.year) : '', paper.venue]
    .filter((part) => part !== '')
    .join(' · ')
}

interface PaperRowProps {
  paper: PaperRecord
  /** Whether the row is selected for a multi-paper comparison. */
  selected: boolean
  onToggleSelect: (id: string) => void
  onOpen: (paper: PaperRecord) => void
  onDeepen: (paper: PaperRecord, newSession: boolean) => void
  onCompare: (paper: PaperRecord, newSession: boolean) => void
  onPin: (paper: PaperRecord) => void
  onPropose: (paper: PaperRecord, newSession: boolean) => void
}

/** One studied paper: a selection checkbox, a click-to-open title, badges, and
 *  the row actions. */
function PaperRow({
  paper,
  selected,
  onToggleSelect,
  onOpen,
  onDeepen,
  onCompare,
  onPin,
  onPropose,
}: PaperRowProps) {
  return (
    <li
      data-testid="paper-row"
      data-paper-id={paper.id}
      data-selected={selected}
      className="rounded-md border border-border bg-background/40"
    >
      <div className="flex w-full items-start gap-1 px-2 py-1">
        <input
          type="checkbox"
          data-testid="paper-select"
          aria-label={`Select ${paper.title}`}
          checked={selected}
          onChange={() => onToggleSelect(paper.id)}
          className="mt-0.5 size-3 shrink-0 accent-info"
        />
        <button
          type="button"
          data-testid="paper-open"
          onClick={() => onOpen(paper)}
          title={`Open ${paper.title}`}
          className="flex min-w-0 flex-1 flex-col items-start gap-0.5 text-left transition-colors hover:bg-muted/50"
        >
          <span className="flex w-full items-center gap-1 text-[12px] font-medium text-foreground">
            {paper.pinned && <Pin data-testid="paper-pinned" className="size-3 shrink-0 text-highlight" />}
            <span className="truncate">{paper.title}</span>
          </span>
          <span className="text-[10px] text-muted-foreground">{metaLine(paper)}</span>
        </button>
      </div>

      <div className="flex items-center gap-1 px-2 pb-1">
        <PaperBadges paper={paper} />
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            data-testid="paper-action-open"
            className={ACTION_BUTTON_CLASS}
            title="Open in a tab"
            onClick={() => onOpen(paper)}
          >
            <FileText className="size-3" />
          </button>
          <button
            type="button"
            data-testid="paper-action-deepen"
            className={ACTION_BUTTON_CLASS}
            title="Go deeper — one mode deeper, append-only (Shift = new session)"
            onClick={(e) => onDeepen(paper, e.shiftKey)}
          >
            <Microscope className="size-3" />
          </button>
          <button
            type="button"
            data-testid="paper-action-compare"
            className={ACTION_BUTTON_CLASS}
            title="Compare with the library (Shift = new session)"
            onClick={(e) => onCompare(paper, e.shiftKey)}
          >
            <GitCompare className="size-3" />
          </button>
          <button
            type="button"
            data-testid="paper-action-pin"
            className={ACTION_BUTTON_CLASS}
            title={paper.pinned ? 'Unpin' : 'Pin'}
            onClick={() => onPin(paper)}
          >
            {paper.pinned ? <PinOff className="size-3" /> : <Pin className="size-3" />}
          </button>
          <button
            type="button"
            data-testid="paper-action-propose"
            className={ACTION_BUTTON_CLASS}
            title="Suggest hypotheses from the recorded gaps (Shift = new session)"
            onClick={(e) => onPropose(paper, e.shiftKey)}
          >
            <Lightbulb className="size-3" />
          </button>
        </div>
      </div>
    </li>
  )
}

function Hint({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <div data-testid={testId} className="px-1 py-6 text-center text-[11px] text-muted-foreground">
      {children}
    </div>
  )
}

/**
 * The Papers segment: the invocation surface (Study-paper field + study-mode
 * selector) over the list of studied papers.
 */
export function PapersView() {
  const { send } = useMessageSender()
  const papers = usePapers()
  const isLoading = usePapersLoading()
  const error = usePapersError()
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = useProjectStore(selectIsNoProject)

  const [reference, setReference] = useState('')
  const [mode, setMode] = useState<StudyMode>('auto')
  // Multi-select for a library comparison. Ids are kept in selection order;
  // the Set is derived for O(1) row lookups (never allocated in a selector).
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds])
  const researchRoot = usePapersResearchRoot()
  // A comparison needs at least two papers — the Compare-selected action stays
  // disabled below that threshold (its tooltip explains what to do).
  const canCompareSelected = selectedIds.length >= 2

  // Drop ids that vanished from the library (a deleted paper must not linger in
  // the selection or keep the Compare button enabled in a stale state).
  useEffect(() => {
    const known = new Set(papers.map((paper) => paper.id))
    setSelectedIds((prev) => {
      const next = prev.filter((id) => known.has(id))
      return next.length === prev.length ? prev : next
    })
  }, [papers])

  const toggleSelect = useCallback((id: string) => {
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((entry) => entry !== id) : [...prev, id],
    )
  }, [])

  const clearSelection = useCallback(() => setSelectedIds([]), [])

  // [22]a pattern (see ResearchQuickActions): send() renders its own send
  // failures in-chat but RETHROWS when the auto-created session fails (the
  // documented splash race) — surface that on the paper store's error line.
  const dispatch = useCallback(
    (prompt: string, skill: string, newSession: boolean) => {
      Promise.resolve(
        send(prompt, [skill], undefined, undefined, { newSession }),
      ).catch((err) => {
        usePaperStore
          .getState()
          .setError(
            `Failed to dispatch ${skill}: ${
              err instanceof Error ? err.message : 'unknown error'
            }`,
          )
      })
    },
    [send],
  )

  const study = useCallback(
    (newSession: boolean) => {
      const ref = reference.trim()
      if (ref === '') return
      dispatch(buildStudyPrompt(ref, mode), STUDY_PAPER_SKILL, newSession)
      setReference('')
    },
    [reference, mode, dispatch],
  )

  // Open the paper's reader workspace as a viewer tab (the synthetic
  // `c0wrk:paper:<slug>` pseudo-path; openPaper also uncollapses the viewer).
  const openPaper = useCallback((paper: PaperRecord) => {
    if (paper.slug === '') return
    useFileViewerStore.getState().openPaper(paper.slug)
  }, [])

  const deepenPaper = useCallback(
    (paper: PaperRecord, newSession: boolean) =>
      dispatch(buildDeepenPrompt(paper), STUDY_PAPER_SKILL, newSession),
    [dispatch],
  )

  const comparePaper = useCallback(
    (paper: PaperRecord, newSession: boolean) =>
      dispatch(buildComparePrompt(paper), STUDY_PAPER_SKILL, newSession),
    [dispatch],
  )

  // Compare the selected papers (≥2), in library order, writing one comparison
  // artifact under the research root's `comparisons/` directory. Clears the
  // selection on dispatch so the gesture cannot be repeated by accident.
  const compareSelected = useCallback(
    (newSession: boolean) => {
      const chosen = papers.filter((paper) => selectedSet.has(paper.id))
      if (chosen.length < 2) return
      dispatch(buildCompareSelectedPrompt(chosen, researchRoot), STUDY_PAPER_SKILL, newSession)
      setSelectedIds([])
    },
    [papers, selectedSet, researchRoot, dispatch],
  )

  const proposeHypothesis = useCallback(
    (paper: PaperRecord, newSession: boolean) => {
      // E5 — auto-pin the paper as prior art (idempotent: a no-op when the paper
      // is already pinned, so repeat gestures never duplicate the pin).
      void ensurePaperPinned(paper.id)
      dispatch(buildProposeHypothesisPrompt(paper), RESEARCH_HYPOTHESIS_SKILL, newSession)
    },
    [dispatch],
  )

  const pinPaper = useCallback((paper: PaperRecord) => {
    void togglePaperPin(paper.id, !paper.pinned)
  }, [])

  if (isNoProject || activeProjectId === null) {
    return (
      <div className="flex min-h-0 flex-1 flex-col" data-testid="papers-view">
        <Hint testId="papers-no-project">
          Open a project to browse its paper library.
        </Hint>
      </div>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="papers-view">
      {/* Invocation surface: the Study-paper field + the study-mode selector */}
      <div className="flex shrink-0 flex-col gap-1 border-b border-border px-1.5 py-1.5">
        <div className="flex items-center gap-1">
          <input
            type="text"
            value={reference}
            aria-label="Study paper"
            data-testid="papers-invoke-input"
            placeholder="arXiv ID, DOI, URL, or PDF path"
            onChange={(e) => setReference(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') study(e.shiftKey)
            }}
            className={INPUT_CLASS}
          />
          <button
            type="button"
            data-testid="papers-invoke-study"
            disabled={reference.trim() === ''}
            title="Study this paper (Shift = new session)"
            onClick={(e) => study(e.shiftKey)}
            className="shrink-0 rounded border border-border bg-background px-1.5 py-0.5 text-[11px] text-foreground transition-colors hover:bg-muted disabled:opacity-50"
          >
            Study
          </button>
        </div>
        <label className="flex items-center gap-1 text-[10px] uppercase tracking-wide text-muted-foreground">
          Mode
          <select
            data-testid="papers-mode-select"
            aria-label="Study mode"
            value={mode}
            onChange={(e) => setMode(e.target.value as StudyMode)}
            className="rounded border border-border bg-background px-1 py-0.5 text-[11px] normal-case tracking-normal text-foreground focus:outline-none"
          >
            {STUDY_MODE_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {error !== null && (
        <div
          data-testid="papers-error"
          className="shrink-0 border-b border-destructive/20 bg-destructive/10 px-2 py-1 text-[11px] text-destructive"
        >
          {error}
        </div>
      )}

      {papers.length > 0 && (
        <div
          data-testid="papers-selection"
          className="flex shrink-0 items-center gap-1 border-b border-border px-1.5 py-1 text-[10px] text-muted-foreground"
        >
          <span data-testid="papers-selection-count">
            {selectedIds.length} selected
          </span>
          {!canCompareSelected && (
            <span data-testid="papers-selection-hint" className="text-muted-foreground/70">
              (select ≥2 to compare)
            </span>
          )}
          <div className="ml-auto flex items-center gap-1">
            {selectedIds.length > 0 && (
              <button
                type="button"
                data-testid="papers-selection-clear"
                onClick={clearSelection}
                className="rounded px-1 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              >
                Clear
              </button>
            )}
            <button
              type="button"
              data-testid="papers-compare-selected"
              disabled={!canCompareSelected}
              title={
                canCompareSelected
                  ? 'Compare the selected papers (Shift = new session)'
                  : 'Select at least 2 papers to compare'
              }
              onClick={(e) => compareSelected(e.shiftKey)}
              className="inline-flex items-center gap-0.5 rounded border border-border bg-background px-1.5 py-0.5 text-[10px] text-foreground transition-colors hover:bg-muted disabled:opacity-50"
            >
              <GitCompare className="size-3" />
              Compare selected
            </button>
          </div>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-auto px-1.5 py-1.5">
        {isLoading && papers.length === 0 ? (
          <Hint testId="papers-loading">Loading…</Hint>
        ) : papers.length === 0 ? (
          <Hint testId="papers-empty">
            No papers studied yet. Paste an arXiv ID, DOI, URL, or PDF path above.
          </Hint>
        ) : (
          <ul data-testid="papers-list" className="flex flex-col gap-1">
            {papers.map((paper) => (
              <PaperRow
                key={paper.id}
                paper={paper}
                selected={selectedSet.has(paper.id)}
                onToggleSelect={toggleSelect}
                onOpen={openPaper}
                onDeepen={deepenPaper}
                onCompare={comparePaper}
                onPin={pinPaper}
                onPropose={proposeHypothesis}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
