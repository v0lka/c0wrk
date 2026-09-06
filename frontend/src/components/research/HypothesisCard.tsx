// The research workspace's editable hypothesis detail card (status /
// result / timebox), extracted from ResearchWorkspace.tsx so the workspace
// file stays a thin layout shell. The card edits a DRAFT snapshotted from the
// selected node (draft state lives in researchStore — see HypothesisCardProps
// and ResearchWorkspace); persistence goes through the t4 UpdateHypothesis
// RPC wired by the workspace's save handler.
import { Loader2, Save, ExternalLink } from 'lucide-react'
import { MiniCodeMirrorField } from '@/components/fileViewer/MiniCodeMirrorField'
import { statusOptions } from './hypothesisStatus'
import type { HypothesisNode, HypothesisDraft } from '@/types/models'

interface HypothesisCardProps {
  node: HypothesisNode
  draft: HypothesisDraft
  saving: boolean
  dirty: boolean
  saveError: string | null
  onChange: (next: HypothesisDraft) => void
  onSave: () => void
  /** Open a hypothesis's markdown card in the file viewer. */
  onOpenCard: (id: string) => void
}

export function HypothesisCard({
  node,
  draft,
  saving,
  dirty,
  saveError,
  onChange,
  onSave,
  onOpenCard,
}: HypothesisCardProps) {
  const parentIds = node.parents ?? []

  return (
    <div
      className="flex h-full min-h-0 flex-col gap-3"
      data-testid="hypothesis-card"
    >
      <div className="shrink-0">
        {/* The card header (id + title) is itself a hypothesis mention: it
            opens this hypothesis's markdown card as a sibling viewer tab.
            The native tooltip carries the full, untruncated title. */}
        <button
          type="button"
          onClick={() => onOpenCard(node.id)}
          title={node.title}
          aria-label={`Open ${node.id} markdown card`}
          className="flex min-w-0 max-w-full items-baseline gap-1.5 rounded-sm text-left underline-offset-2 hover:underline"
        >
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
            {node.id}
          </span>
          <span className="truncate text-xs font-semibold">{node.title}</span>
          <ExternalLink className="size-3 shrink-0 self-center text-muted-foreground/60" />
        </button>
        {parentIds.length > 0 && (
          <p className="mt-0.5 flex flex-wrap items-baseline gap-1 text-[11px] text-muted-foreground/70">
            <span>parents:</span>
            {parentIds.map((p, i) => (
              <span key={p} className="flex items-baseline">
                {i > 0 && <span className="text-muted-foreground/50">,</span>}
                <button
                  type="button"
                  onClick={() => onOpenCard(p)}
                  title={`Open ${p} markdown card`}
                  className="rounded-sm font-mono text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                >
                  {p}
                </button>
              </span>
            ))}
          </p>
        )}
      </div>

      <label className="flex shrink-0 flex-col gap-1">
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
          Status
        </span>
        {/* Only the current status and its legal transition targets are
            offered — the backend state machine (writer.go) rejects every
            other jump, so a wider list would only produce failed saves. */}
        <select
          value={draft.status}
          onChange={(e) => onChange({ ...draft, status: e.target.value })}
          aria-label="Hypothesis status"
          className="h-8 w-full rounded-md border border-input bg-background px-2 text-xs outline-none focus:border-primary"
        >
          {statusOptions(draft.status).map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>

      <label className="flex shrink-0 flex-col gap-1">
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
          Timebox
        </span>
        <input
          type="text"
          value={draft.timebox}
          onChange={(e) => onChange({ ...draft, timebox: e.target.value })}
          aria-label="Hypothesis timebox"
          placeholder="e.g. 2 weeks"
          className="h-8 w-full rounded-md border border-input bg-background px-2 text-xs outline-none focus:border-primary"
        />
      </label>

      {/* Result: fills every remaining vertical pixel of the sidebar. The
          markdown-aware CodeMirror field brings syntax highlighting and the
          project-wide custom scrollbar (cm-viewer-container). */}
      <label className="flex min-h-0 flex-1 flex-col gap-1" data-testid="hypothesis-result-field">
        <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">
          Result
        </span>
        <MiniCodeMirrorField
          value={draft.result}
          onChange={(result) => onChange({ ...draft, result })}
          placeholder="Finding / outcome…"
          lineWrapping
          className="min-h-0 max-h-none flex-1"
        />
      </label>

      {saveError && (
        <p className="shrink-0 text-xs text-destructive" role="alert">
          {saveError}
        </p>
      )}

      <button
        type="button"
        onClick={onSave}
        disabled={saving || !dirty}
        data-testid="hypothesis-save"
        className="inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md bg-primary px-2 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
      >
        {saving ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <Save className="size-3.5" />
        )}
        Save
      </button>
    </div>
  )
}
