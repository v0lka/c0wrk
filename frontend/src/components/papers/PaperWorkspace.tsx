// Paper workspace (область просмотра результатов) — the file viewer's content
// for a `c0wrk:paper:<slug>` virtual tab.
//
// It renders one studied paper as a set of sections:
//   Обзор      — identity card + source anchors (E1) + the critical layer (E2)
//   Заметка    — note.md
//   Appraisal  — appraisal.md
//   Compare    — the library comparisons (`<research-root>/comparisons/`) this
//                paper takes part in, else the per-paper comparison artifact
//   Flashcards — flashcards.md
//   Источник   — the extracted source text, with anchor scrolling (E1)
//   Literature — the literature-context artifact
//
// The tab is virtual (never persisted). The paper record comes from paperStore
// (its slug is the pseudo-path suffix); the artifact files are read from the
// paper's directory through the workspace RPCs.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { BookOpen, FileText, Lightbulb, Microscope } from 'lucide-react'
import type { PaperAnchor, PaperRecord } from '@/api/papers'
import {
  usePaperStore,
  usePaperBySlug,
  usePapersError,
  usePapersResearchRoot,
  selectPapersSyncAt,
  ensurePaperPinned,
} from '@/stores/paperStore'
import { useMessageSender } from '@/hooks/useMessageSender'
import { resolveAnchor } from '@/lib/paperAnchors'
import { comparisonMentionsPaper } from '@/lib/paperComparison'
import {
  MAX_RED_FLAG_CHIPS,
  parseCriticalLayer,
  type CriticalLayer,
  type RedFlagChip,
  type UncertaintyChip,
} from '@/lib/paperWidgets'
import { cn } from '@/lib/utils'
import { PaperOverview } from './PaperOverview'
import { PaperSourceView, type PendingAnchor } from './PaperSourceView'
import { PaperMarkdownSection } from './PaperMarkdownSection'
import { PaperLiterature } from './PaperLiterature'
import { FlashcardsReview } from './FlashcardsReview'
import { CompareMatrix } from './CompareMatrix'
import { useComparisons } from './useComparisons'
import { usePaperArtifacts, type PaperArtifacts } from './usePaperArtifacts'
import { PAPER_WORKSPACE_SECTIONS, type PaperSection } from './paperSections'
import {
  RESEARCH_HYPOTHESIS_SKILL,
  STUDY_PAPER_SKILL,
  buildDeepenPrompt,
  buildProposeHypothesisPrompt,
} from './paperActions'

const ACTION_BUTTON_CLASS =
  'inline-flex shrink-0 items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground'

function joinPath(dir: string, name: string): string {
  return `${dir.replace(/[\\/]+$/, '')}/${name}`
}

/** The record's normalized red flags as chips (fallback when no markdown was found). */
function recordRedFlags(paper: PaperRecord | null): RedFlagChip[] {
  if (paper === null) return []
  return paper.red_flags
    .slice(0, MAX_RED_FLAG_CHIPS)
    .map((flag) => ({ flag: flag.flag, detail: flag.detail, severity: flag.severity }))
}

/** The record's normalized uncertainties as chips (same fallback). */
function recordUncertainties(paper: PaperRecord | null): UncertaintyChip[] {
  if (paper === null) return []
  return paper.uncertainties.map((item) => ({ item: item.item, detail: item.detail }))
}

/**
 * Build the critical layer: parse it from the note (authoritative) and the
 * appraisal, and fall back to the backend-normalized record lists when neither
 * artifact carries a critical-layer table. Red flags are capped at three.
 */
function buildCriticalLayer(
  paper: PaperRecord | null,
  artifacts: PaperArtifacts,
): CriticalLayer {
  const parsed = parseCriticalLayer(artifacts.note.content, artifacts.appraisal.content)
  return {
    evidence: parsed.evidence,
    redFlags:
      parsed.redFlags.length > 0
        ? parsed.redFlags.slice(0, MAX_RED_FLAG_CHIPS)
        : recordRedFlags(paper),
    uncertainties:
      parsed.uncertainties.length > 0 ? parsed.uncertainties : recordUncertainties(paper),
  }
}

function NotFound({ slug }: { slug: string }) {
  return (
    <div
      data-testid="paper-not-found"
      className="flex h-full min-h-0 flex-col items-center justify-center gap-1 px-4 text-center"
    >
      <FileText className="size-5 text-muted-foreground" />
      <p className="text-[12px] text-foreground">Paper not found in the loaded library</p>
      <p className="text-[10px] text-muted-foreground">{slug}</p>
    </div>
  )
}

export function PaperWorkspace({ slug }: { slug: string }) {
  const paper = usePaperBySlug(slug)
  const syncAt = usePaperStore(selectPapersSyncAt)
  // A dispatch failure ("Go deeper" / "Hypotheses from gaps") is recorded on the
  // paper store; this tab must render it itself — its only other renderer is the
  // Research panel's Papers segment, a different surface the user may never open.
  const error = usePapersError()
  const researchRoot = usePapersResearchRoot()
  const dir = paper?.dir ?? ''
  const artifacts = usePaperArtifacts(dir, syncAt)
  // Multi-paper comparisons live at the research-root level (not in the paper
  // directory); the Compare section shows only those this paper takes part in.
  const comparisons = useComparisons(researchRoot, syncAt)
  const { send } = useMessageSender()
  const [section, setSection] = useState<PaperSection>('overview')
  const [pending, setPending] = useState<PendingAnchor | null>(null)
  const [missedIndex, setMissedIndex] = useState<number | null>(null)
  const nonceRef = useRef(0)

  // A different paper in the same tab starts from a clean view.
  useEffect(() => {
    setSection('overview')
    setPending(null)
    setMissedIndex(null)
  }, [slug])

  const layer = useMemo(() => buildCriticalLayer(paper, artifacts), [paper, artifacts])

  // The comparisons this paper takes part in — matched against the recorded
  // id / slug / card path / identifier / title. Derived with useMemo (outside
  // any store selector).
  const relevantComparisons = useMemo(
    () =>
      paper === null
        ? []
        : comparisons.items.filter((item) => comparisonMentionsPaper(item.content, paper)),
    [comparisons.items, paper],
  )

  // Comparisons whose file could not be read (content '' so they never match a
  // paper). Surfaced as a notice instead of silently falling through to the
  // empty state — a read error must not look like "no comparisons".
  const erroredComparisons = useMemo(
    () => comparisons.items.filter((item) => item.error !== null),
    [comparisons.items],
  )

  const onAnchorSelect = useCallback(
    (anchor: PaperAnchor, index: number) => {
      const hit = resolveAnchor(artifacts.source.content, anchor)
      if (hit === null) {
        // Honest fallback: record which anchor failed and never scroll anywhere.
        setMissedIndex(index)
        return
      }
      setMissedIndex(null)
      nonceRef.current += 1
      setPending({ line: hit.line, nonce: nonceRef.current })
      setSection('source')
    },
    [artifacts.source.content],
  )

  // [22]a pattern (see PapersView): send() renders its own send failures in-chat
  // but RETHROWS when the auto-created session fails — surface that on the
  // paper store's error line instead of dropping it.
  const dispatch = useCallback(
    (prompt: string, skill: string) => {
      // Snapshot the loaded project so a switch while the dispatch is in flight
      // cannot write this failure into the new project's error slot (the store's
      // own async writers guard for exactly this).
      const projectIdBefore = usePaperStore.getState().projectId
      const stillSameProject = (): boolean =>
        usePaperStore.getState().projectId === projectIdBefore
      void Promise.resolve(send(prompt, [skill])).then(
        () => {
          // A successful (re)dispatch clears a stale failure banner.
          if (stillSameProject()) usePaperStore.getState().setError(null)
        },
        (err) => {
          if (!stillSameProject()) return
          usePaperStore
            .getState()
            .setError(
              `Failed to dispatch ${skill}: ${
                err instanceof Error ? err.message : 'unknown error'
              }`,
            )
        },
      )
    },
    [send],
  )

  // E4 — "Go deeper": re-dispatch the study-paper skill on the SAME card one
  // mode deeper. The skill appends its sections; the library sync then rebuilds
  // this workspace's sections (see the syncAt refresh key above).
  const onDeepen = useCallback(() => {
    if (paper === null) return
    dispatch(buildDeepenPrompt(paper), STUDY_PAPER_SKILL)
  }, [paper, dispatch])

  // E5 — "Suggest hypotheses from gaps": formulate hypotheses from the paper's
  // recorded uncertainties and auto-pin the paper as prior art (idempotent).
  const onProposeGaps = useCallback(() => {
    if (paper === null) return
    void ensurePaperPinned(paper.id)
    dispatch(buildProposeHypothesisPrompt(paper), RESEARCH_HYPOTHESIS_SKILL)
  }, [paper, dispatch])

  if (paper === null) return <NotFound slug={slug} />

  const baseFilePath = (name: string): string | null => (dir === '' ? null : joinPath(dir, name))
  const anchors = paper.anchors

  return (
    <div data-testid="paper-workspace" className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/30 px-2 py-1">
        <BookOpen className="size-3.5 shrink-0 text-info" />
        <span className="min-w-0 flex-1 truncate text-xs font-medium" title={paper.title}>
          {paper.title !== '' ? paper.title : paper.slug}
        </span>
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            data-testid="paper-go-deeper"
            title="Go deeper — study the same card one mode deeper (append-only)"
            onClick={onDeepen}
            className={ACTION_BUTTON_CLASS}
          >
            <Microscope className="size-3" />
            Go deeper
          </button>
          <button
            type="button"
            data-testid="paper-suggest-hypotheses"
            title="Suggest hypotheses from the recorded gaps (auto-pins the paper as prior art)"
            onClick={onProposeGaps}
            className={ACTION_BUTTON_CLASS}
          >
            <Lightbulb className="size-3" />
            Hypotheses from gaps
          </button>
        </div>
        {paper.verdict !== '' && (
          <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">
            {paper.verdict}
          </span>
        )}
      </header>

      {error !== null && (
        <div
          data-testid="paper-workspace-error"
          className="shrink-0 border-b border-destructive/20 bg-destructive/10 px-2 py-1 text-[11px] text-destructive"
        >
          {error}
        </div>
      )}

      <nav
        data-testid="paper-sections"
        className="flex shrink-0 items-center gap-0.5 overflow-x-auto no-scrollbar border-b border-border px-1 py-0.5"
      >
        {PAPER_WORKSPACE_SECTIONS.map((s) => (
          <button
            key={s.id}
            type="button"
            data-testid={`paper-section-${s.id}`}
            data-active={section === s.id}
            aria-current={section === s.id ? 'true' : undefined}
            onClick={() => setSection(s.id)}
            className={cn(
              'shrink-0 rounded px-1.5 py-0.5 text-[11px] transition-colors',
              section === s.id
                ? 'bg-background text-foreground'
                : 'text-muted-foreground hover:bg-muted/40 hover:text-foreground',
            )}
          >
            {s.label}
          </button>
        ))}
      </nav>

      <div className="flex min-h-0 flex-1 flex-col">
        {section === 'overview' && (
          <PaperOverview
            paper={paper}
            layer={layer}
            missedIndex={missedIndex}
            onAnchorSelect={onAnchorSelect}
          />
        )}
        {section === 'note' && (
          <PaperMarkdownSection
            artifact={artifacts.note}
            testId="paper-note"
            emptyText="No note recorded for this paper yet."
            baseFilePath={baseFilePath(artifacts.note.fileName)}
          />
        )}
        {section === 'appraisal' && (
          <PaperMarkdownSection
            artifact={artifacts.appraisal}
            testId="paper-appraisal"
            emptyText="No appraisal recorded for this paper yet."
            baseFilePath={baseFilePath(artifacts.appraisal.fileName)}
          />
        )}
        {section === 'compare' &&
          (comparisons.loading ? (
            <p
              data-testid="paper-compare-loading"
              className="px-3 py-4 text-center text-[11px] text-muted-foreground"
            >
              Loading…
            </p>
          ) : (
            <>
              {erroredComparisons.length > 0 && (
                <div
                  data-testid="paper-compare-error"
                  className="shrink-0 border-b border-destructive/20 bg-destructive/10 px-2 py-1 text-[11px] text-destructive"
                >
                  {erroredComparisons.map((comparison) => (
                    <p key={comparison.slug}>could not read {comparison.slug}.md</p>
                  ))}
                </div>
              )}
              {relevantComparisons.length > 0 ? (
                <div
                  data-testid="paper-compare-comparisons"
                  className="min-h-0 flex-1 overflow-auto custom-scrollbar"
                >
                  {relevantComparisons.map((comparison) => (
                    <CompareMatrix
                      key={comparison.slug}
                      slug={comparison.slug}
                      content={comparison.content}
                    />
                  ))}
                </div>
              ) : (
                // No library comparison involves this paper: fall back to the
                // paper's own recorded comparison artifact (the older per-paper
                // form).
                <PaperMarkdownSection
                  artifact={artifacts.compare}
                  testId="paper-compare"
                  emptyText="No comparison recorded for this paper yet."
                  baseFilePath={baseFilePath(artifacts.compare.fileName)}
                />
              )}
            </>
          ))}
        {section === 'flashcards' && (
          <FlashcardsReview
            artifact={artifacts.flashcards}
            testId="paper-flashcards"
            emptyText="No flashcards recorded for this paper yet."
            baseFilePath={baseFilePath(artifacts.flashcards.fileName)}
            paperId={paper.id}
            resetKey={paper.id}
          />
        )}
        {section === 'source' && (
          <PaperSourceView
            artifact={artifacts.source}
            anchors={anchors}
            pending={pending}
            missedIndex={missedIndex}
            onAnchorSelect={onAnchorSelect}
          />
        )}
        {section === 'literature' && (
          <PaperLiterature
            paper={paper}
            markdownArtifact={artifacts.literature}
            baseFilePath={baseFilePath(artifacts.literature.fileName)}
          />
        )}
      </div>
    </div>
  )
}
