// PaperLiterature — the paper workspace's Literature section. It renders the
// paper's literature NEIGHBOURHOOD as a DAG (reusing the research canvas) plus
// the free-form literature context note, and can run the study-paper
// `literature.py` helper through the backend (managed Python) to (re)build
// `<paper-dir>/literature.json`.
//
// Degradation is explicit and honest: a missing/unreadable/invalid JSON, and
// every non-ok run outcome (offline / unresolved / rate-limited / no managed
// Python / no script / no seed), render a distinct message rather than an empty
// graph. Nothing is fabricated — the graph is drawn only from a parsed file.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Network, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { PaperRecord, PaperLiteratureResult, PaperLiteratureStatus } from '@/api/papers'
import { runPaperLiterature } from '@/api/papers'
import { usePaperStore } from '@/stores/paperStore'
import {
  literatureGeneratedAtLabel,
  literatureKindLabel,
  literatureToGraph,
  parseLiteratureJson,
  workDisplayTitle,
  type LiteratureGraphModel,
} from '@/lib/literatureGraph'
import { cn } from '@/lib/utils'
import { LiteratureGraph } from './LiteratureGraph'
import { PaperMarkdownSection } from './PaperMarkdownSection'
import { usePaperLiterature } from './usePaperLiterature'
import type { PaperArtifact } from './usePaperArtifacts'

/** A short, user-facing headline per degraded run status. */
const RUN_LABEL: Record<PaperLiteratureStatus, string> = {  ok: 'Literature lookup complete',
  offline: 'Network unavailable',
  unresolved: 'Seed could not be resolved',
  rate_limited: 'Source rate limit reached',
  no_python: 'Managed Python unavailable',
  no_script: 'Literature helper missing',
  no_seed: 'No identifier to seed the lookup',
  error: 'Literature lookup failed',
}

/** Shown when no usable graph exists (no file yet, or an empty projection). */
const EMPTY_MESSAGE =
  'No literature graph recorded for this paper yet. Run the lookup (it needs network access to OpenAlex/Crossref/arXiv) to build one.'

/** The managed-Python install path, surfaced when a run reports `no_python`. */
const NO_PYTHON_HINT =
  "c0wrk's tool manager installs its managed Python automatically on first launch (it needs " +
  'network access). Confirm c0wrk can reach the network, then retry — or restart c0wrk to let ' +
  'the install run again.'

/**
 * The `no_python` outcome is not a dead end: c0wrk's tool manager provisions the
 * managed Python on its own. This affordance states that path and offers a
 * one-click retry, so the user never stares at a bare message.
 */
function NoPythonAffordance({ onRetry, running }: { onRetry: () => void; running: boolean }) {
  return (
    <div
      data-testid="literature-no-python"
      className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/20 px-2 py-1.5 text-[11px]"
    >
      <p className="text-muted-foreground">{NO_PYTHON_HINT}</p>
      <Button
        variant="secondary"
        size="sm"
        className="ml-auto h-6 shrink-0 gap-1 px-2 text-[11px]"
        onClick={onRetry}
        disabled={running}
        data-testid="literature-no-python-retry"
      >
        <RefreshCw className={cn('size-3', running && 'animate-spin')} />
        {running ? 'Refreshing…' : 'Retry'}
      </Button>
    </div>
  )
}

function StateMessage({
  testId,
  tone = 'muted',
  children,
}: {
  testId: string
  tone?: 'muted' | 'error'
  children: ReactNode
}) {
  return (
    <div data-testid={testId} className="flex h-full min-h-0 items-center justify-center px-4">
      <p
        className={cn(
          'max-w-[42ch] text-center text-[11px]',
          tone === 'error' ? 'text-destructive' : 'text-muted-foreground',
        )}
      >
        {children}
      </p>
    </div>
  )
}

/** The selected node's card: kind, title, year, DOI, authors, and any markers. */
function NodeDetail({
  model,
  selectedId,
}: {
  model: LiteratureGraphModel | null
  selectedId: string | null
}) {
  if (model === null || selectedId === null) return null
  const work = model.works[selectedId]
  const kind = model.kinds[selectedId]
  if (work === undefined || kind === undefined) return null
  return (
    <div
      data-testid="literature-detail"
      data-kind={kind}
      className="shrink-0 border-t border-border bg-secondary/20 px-2 py-1.5 text-[11px]"
    >
      <div className="flex items-center gap-2">
        <span className="shrink-0 rounded bg-muted px-1 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
          {literatureKindLabel(kind)}
        </span>
        <span className="truncate font-medium" title={workDisplayTitle(work)}>
          {workDisplayTitle(work)}
        </span>
        {work.year !== null && (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground">
            {work.year}
          </span>
        )}
      </div>
      {work.doi.trim() !== '' && (
        <p className="mt-0.5 truncate text-[10px] text-muted-foreground">DOI: {work.doi}</p>
      )}
      {work.authors.length > 0 && (
        <p className="truncate text-[10px] text-muted-foreground">{work.authors.join(', ')}</p>
      )}
      {work.reasons.length > 0 && (
        <p className="mt-0.5 text-[10px] text-destructive">
          Contradiction markers: {work.reasons.join(', ')}
        </p>
      )}
    </div>
  )
}

export interface PaperLiteratureProps {
  paper: PaperRecord
  /** The literature context Markdown (literature.md / literature-context.md). */
  markdownArtifact: PaperArtifact
  baseFilePath?: string | null
}

export function PaperLiterature({ paper, markdownArtifact, baseFilePath }: PaperLiteratureProps) {
  const projectId = usePaperStore((s) => s.projectId)
  const { artifact, reload } = usePaperLiterature(paper.dir)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [run, setRun] = useState<PaperLiteratureResult | null>(null)
  const [running, setRunning] = useState(false)
  // Identity token of the paper the in-flight run belongs to. Bumped whenever
  // the displayed paper changes, so a ~90 s-bounded RPC that resolves after a
  // paper switch can never apply paper A's run status under paper B.
  const runIdentityRef = useRef(0)

  // A different paper in the same tab starts clean.
  useEffect(() => {
    runIdentityRef.current += 1
    setSelectedId(null)
    setRun(null)
    setRunning(false)
  }, [paper.slug])

  const parsed = useMemo(() => parseLiteratureJson(artifact.raw), [artifact.raw])
  const model = useMemo(
    () => (parsed.record !== null ? literatureToGraph(parsed.record) : null),
    [parsed.record],
  )
  const generatedLabel = useMemo(
    () => (parsed.record !== null ? literatureGeneratedAtLabel(parsed.record.generatedAt) : ''),
    [parsed.record],
  )

  const onRun = useCallback(() => {
    if (projectId === null) {
      setRun({ status: 'error', message: 'No active project.', path: '', content: '' })
      return
    }
    // Capture the paper identity at click time; the result is dropped unless the
    // displayed paper is still the one that started the run (mirrors the
    // `cancelled` flag in usePaperLiterature and the projectId re-check in the
    // store's async writers).
    const identity = runIdentityRef.current
    const isCurrent = (): boolean => runIdentityRef.current === identity
    setRunning(true)
    void (async () => {
      try {
        const result = await runPaperLiterature(projectId, paper.id)
        if (!isCurrent()) return
        setRun(result)
        if (result.status === 'ok') reload()
      } catch (err) {
        if (!isCurrent()) return
        setRun({
          status: 'error',
          message: err instanceof Error ? err.message : 'The lookup request failed.',
          path: '',
          content: '',
        })
      } finally {
        if (isCurrent()) setRunning(false)
      }
    })()
  }, [projectId, paper.id, reload])

  const degraded = run !== null && run.status !== 'ok'

  return (
    <div data-testid="paper-literature" className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/20 px-2 py-1">
        <Network className="size-3.5 shrink-0 text-info" />
        <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
          {parsed.record !== null
            ? `${parsed.record.predecessors.length} predecessors · ${parsed.record.citing.length} citing · ${parsed.record.contradictions.length} contradictions`
            : 'Literature neighbourhood'}
        </span>
        {generatedLabel !== '' && (
          <span
            data-testid="literature-generated"
            title={parsed.record?.generatedAt ?? ''}
            className="shrink-0 text-[10px] tabular-nums text-muted-foreground/70"
          >
            {generatedLabel}
          </span>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="h-6 shrink-0 gap-1 px-2 text-[11px]"
          onClick={onRun}
          disabled={running}
          data-testid="literature-run"
        >
          <RefreshCw className={cn('size-3', running && 'animate-spin')} />
          {running ? 'Refreshing…' : 'Refresh'}
        </Button>
      </header>

      {run !== null && (
        <>
          <div
            data-testid="literature-run-status"
            data-status={run.status}
            className={cn(
              'shrink-0 border-b border-border px-2 py-1 text-[11px]',
              degraded ? 'text-destructive' : 'text-success',
            )}
          >
            <span className="font-medium">{RUN_LABEL[run.status]}</span>
            {run.message.trim() !== '' && (
              <span className="text-muted-foreground"> — {run.message}</span>
            )}
          </div>
          {run.status === 'no_python' && <NoPythonAffordance onRetry={onRun} running={running} />}
        </>
      )}

      <div className="flex min-h-0 flex-1 flex-col">
        <div className="relative min-h-0 flex-1">
          {artifact.loading ? (
            <StateMessage testId="literature-loading">Loading literature graph…</StateMessage>
          ) : artifact.readError !== null ? (
            <StateMessage testId="literature-read-error" tone="error">
              Could not read literature.json: {artifact.readError}
            </StateMessage>
          ) : artifact.missing ? (
            <StateMessage testId="literature-empty">{EMPTY_MESSAGE}</StateMessage>
          ) : parsed.error !== null ? (
            <StateMessage testId="literature-parse-error" tone="error">
              literature.json could not be parsed: {parsed.error}
            </StateMessage>
          ) : model === null || model.graph.nodes.length === 0 ? (
            <StateMessage testId="literature-empty">{EMPTY_MESSAGE}</StateMessage>
          ) : (
            <LiteratureGraph model={model} selectedId={selectedId} onSelect={setSelectedId} />
          )}
        </div>

        <NodeDetail model={model} selectedId={selectedId} />
      </div>

      {(markdownArtifact.content !== '' || markdownArtifact.error !== null) && (
        <div className="h-1/3 shrink-0 border-t border-border">
          <PaperMarkdownSection
            artifact={markdownArtifact}
            testId="paper-literature"
            emptyText="No literature context recorded for this paper yet."
            baseFilePath={baseFilePath}
          />
        </div>
      )}
    </div>
  )
}
