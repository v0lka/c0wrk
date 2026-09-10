// The research workspace's editable hypothesis detail card (title / parents /
// status / decision / statement / verification criterion / experiment notes /
// timebox / result), extracted from ResearchWorkspace.tsx so the workspace
// file stays a thin layout shell. The card edits a DRAFT snapshotted from the
// selected node (draft state lives in researchStore — see HypothesisCardProps
// and ResearchWorkspace); persistence goes through the t4 UpdateHypothesis
// RPC wired by the workspace's save handler. The section order mirrors the
// methodology's card template (writer.go buildCardContent): field table
// (title / parents / status / decision / timebox), then Statement,
// Verification Criterion, Experiment Notes, Result. The card is one
// natural-height flow: the workspace's resizable card panel owns the
// vertical overflow, so the header, field table, sections, and Save all
// scroll together — nothing is pinned outside the scroll region.
import { Loader2, Save, ExternalLink } from 'lucide-react'
import { MiniCodeMirrorField } from '@/components/fileViewer/MiniCodeMirrorField'
import { statusOptions } from './hypothesisStatus'
import { decisionOptions, decisionLabel } from './hypothesisDecision'
import { parseParentIds } from './researchWorkspaceUtils'
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

const inputCls =
  'h-8 w-full rounded-md border border-input bg-background px-2 text-xs outline-none focus:border-primary'

/**
 * One labelled editor of a long-form card section (statement / criterion /
 * notes / result) — a markdown-aware CodeMirror field. Each field grows to
 * its content (`max-h-none`) so nothing is clipped and the card panel's
 * scroll region (not the field) owns the vertical overflow; without that the
 * field must carry its own scroll and the two scrollbars fight. Never give
 * the field `flex-1`/`min-h-0` inside a flex column — CodeMirror's `.cm-editor`
 * is `height:100%` of its container, so a flex-squeezed container collapses
 * the editor and its content stacks over the next section (the W-57 overlap).
 */
function SectionField({
  label,
  value,
  placeholder,
  onChange,
}: {
  label: string
  value: string
  placeholder: string
  onChange: (v: string) => void
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <MiniCodeMirrorField
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        ariaLabel={`Hypothesis ${label.toLowerCase()}`}
        lineWrapping
        className="max-h-none"
      />
    </label>
  )
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
  const parentIds = parseParentIds(draft.parents)

  return (
    <div className="flex shrink-0 flex-col gap-3" data-testid="hypothesis-card">
      {/* Card header: the id chip (itself a hypothesis mention — clicking it
          opens this hypothesis's markdown card) plus the editable title. */}
      <div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => onOpenCard(node.id)}
            title={`Open ${node.id} markdown card`}
            aria-label={`Open ${node.id} markdown card`}
            className="flex shrink-0 items-center gap-1 rounded-sm font-mono text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          >
            {node.id}
            <ExternalLink className="size-3 text-muted-foreground/60" />
          </button>
          {/* Title: the card H1's editable counterpart. */}
          <input
            type="text"
            value={draft.title}
            onChange={(e) => onChange({ ...draft, title: e.target.value })}
            aria-label="Hypothesis title"
            placeholder="Short label…"
            className={inputCls}
          />
        </div>
        {/* Parents: comma-separated hypothesis ids (canonicalized on save;
            unknown parents are rejected server-side). */}
        <label className="mt-2 flex flex-col gap-1">
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
            Parents
          </span>
          <input
            type="text"
            value={draft.parents}
            onChange={(e) => onChange({ ...draft, parents: e.target.value })}
            aria-label="Hypothesis parents"
            placeholder="e.g. H-001, H-002 (empty for a root)"
            className={inputCls}
          />
          {parentIds.length > 0 && (
            <p className="flex flex-wrap items-baseline gap-1 text-[11px] text-muted-foreground/70">
              <span>resolves:</span>
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
        </label>
      </div>

      {/* Field-table counterparts: status (state machine), decision (fixed
          vocabulary), timebox. */}
      <div className="grid grid-cols-3 gap-2">
        <label className="flex flex-col gap-1">
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
            className={`${inputCls} h-8`}
          >
            {statusOptions(draft.status).map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
            Decision
          </span>
          {/* The methodology's fixed vocabulary (continue / pivot / kill /
              fork — research-decision skill), so a select, not free text.
              The undecided option ('') clears the field; a legacy free-text
              value from an older card stays visible (first option) so it
              remains re-savable and can be replaced. */}
          <select
            value={draft.decision}
            onChange={(e) => onChange({ ...draft, decision: e.target.value })}
            aria-label="Hypothesis decision"
            className={`${inputCls} h-8`}
          >
            {decisionOptions(draft.decision).map((d) => (
              <option key={d} value={d}>
                {decisionLabel(d)}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
            Timebox
          </span>
          <input
            type="text"
            value={draft.timebox}
            onChange={(e) => onChange({ ...draft, timebox: e.target.value })}
            aria-label="Hypothesis timebox"
            placeholder="e.g. 2 weeks"
            className={inputCls}
          />
        </label>
      </div>

      {/* Long-form sections, in the methodology card's order. The whole card
          is one flow — the workspace's card panel owns the scroll region, so
          these (and the header, field table, and Save) scroll together. */}
      <div className="flex flex-col gap-3">
        <SectionField
          label="Statement"
          value={draft.statement}
          placeholder="Falsifiable assertion…"
          onChange={(statement) => onChange({ ...draft, statement })}
        />
        <SectionField
          label="Verification Criterion"
          value={draft.verification_criterion}
          placeholder="What constitutes confirmation…"
          onChange={(verification_criterion) =>
            onChange({ ...draft, verification_criterion })
          }
        />
        <SectionField
          label="Experiment Notes"
          value={draft.experiment_notes}
          placeholder="Observations, setup, logs…"
          onChange={(experiment_notes) => onChange({ ...draft, experiment_notes })}
        />
        <SectionField
          label="Result"
          value={draft.result}
          placeholder="Finding / outcome…"
          onChange={(result) => onChange({ ...draft, result })}
        />
      </div>

      {saveError && (
        <p className="text-xs text-destructive" role="alert">
          {saveError}
        </p>
      )}

      <button
        type="button"
        onClick={onSave}
        disabled={saving || !dirty}
        data-testid="hypothesis-save"
        className="inline-flex items-center justify-center gap-1.5 rounded-md bg-primary px-2 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
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
