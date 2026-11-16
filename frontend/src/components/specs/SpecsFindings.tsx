// The findings section of the active framework: every failed DECLARED check,
// in model order. Renders NOTHING when clean — the header's badge is the clean
// signal; this list appears only when there is something to see.
//
// Both the label and the kind of a finding belong to the framework document:
// `message` is the check's declared operator-facing explanation (rendered when
// present) and `kind` its own token (readable-ized otherwise). A framework the
// frontend has never heard of renders its own words, and the per-finding fix
// badge is a BUTTON only when that framework declared the findings-fix gesture
// — otherwise it renders inert.

import { AlertTriangle } from 'lucide-react'
import type { SDDAction, SDDFinding, SDDView } from '@/types/models'
import { CopyIconButton } from '@/components/CopyIconButton'
import { findingLabel } from './specsPaths'
import {
  NO_FIX_FINDINGS_DECLARED_REASON,
  findingLine,
  findingsFixDispatch,
  inertActionReason,
  type SpecsDispatch,
} from './specsActions'

/** One finding card, cascading on two rows: row 1 carries the fix badge (the
 *  icon + the finding's label) and the file link (opens in the file viewer),
 *  row 2 the detail text with the hover-revealed copy button pinned to its
 *  trailing edge — copying the verbatim findingLine, the exact bullet the
 *  dispatched fix prompt carries, so the clipboard never drifts from what the
 *  task sees. The reveal is opacity-only and the wrapper always occupies its
 *  layout slot: hovering must not move a single pixel (the MessageFooter
 *  pattern). */
function FindingRow({
  finding,
  fixAction,
  onOpen,
  onDispatch,
}: {
  finding: SDDFinding
  fixAction: SDDAction | null
  onOpen: (modelPath: string) => void
  onDispatch: (dispatch: SpecsDispatch, newSession: boolean) => void
}) {
  const label = findingLabel(finding)
  const dispatch = findingsFixDispatch(fixAction, [finding])
  // Three states, one truth: dispatchable → a button; declared → inert WITH
  // THE REASON (no skill to run, or requirements unmet — the workflow's next
  // step stays visible); nothing declared → inert with the declaration fact.
  const inertTitle =
    fixAction !== null
      ? inertActionReason(fixAction)
      : NO_FIX_FINDINGS_DECLARED_REASON
  return (
    <div
      data-testid="specs-finding"
      data-kind={finding.kind}
      className="group flex flex-col gap-0.5 rounded bg-secondary/40 px-1.5 py-1"
    >
      <div className="flex items-center gap-1.5 text-xs">
        {dispatch !== null ? (
          <button
            type="button"
            data-testid="specs-finding-fix"
            data-action={fixAction?.kind ?? ''}
            data-skill={dispatch.skill}
            title="Fix this finding in the chat — Shift = new session"
            onClick={(e) => onDispatch(dispatch, e.shiftKey)}
            className="inline-flex min-w-0 shrink items-center gap-1 rounded bg-warning/10 px-1 text-xs text-warning transition-colors hover:bg-warning/20"
          >
            <AlertTriangle className="size-3 shrink-0" />
            <span className="truncate">{label}</span>
          </button>
        ) : (
          <span
            data-testid="specs-finding-fix"
            data-inert="true"
            title={inertTitle}
            className="inline-flex min-w-0 shrink items-center gap-1 rounded bg-warning/10 px-1 text-xs text-warning"
          >
            <AlertTriangle className="size-3 shrink-0" />
            <span className="truncate">{label}</span>
          </span>
        )}
        <button
          type="button"
          data-testid="specs-finding-file"
          data-file={finding.file}
          title={finding.file}
          onClick={() => onOpen(finding.file)}
          className="shrink-0 rounded px-1 text-info underline decoration-info/40 underline-offset-2 transition-colors hover:bg-muted"
        >
          {finding.file.split('/').pop() ?? finding.file}
        </button>
      </div>
      <div className="flex items-end gap-1 text-xs">
        <div
          data-testid="specs-finding-detail"
          className="min-w-0 flex-1 break-words leading-relaxed text-muted-foreground"
        >
          {finding.detail}
        </div>
        <div
          data-testid="specs-finding-copy"
          className="ml-auto shrink-0 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover:opacity-100"
        >
          <CopyIconButton text={findingLine(finding)} label="Copy finding line" />
        </div>
      </div>
    </div>
  )
}

/** The findings (or checks) section: every failed declared check, in model
 *  order, under the section's own declared heading. */
export function SpecsFindings({
  section,
  findings,
  fixAction,
  onOpen,
  onDispatch,
}: {
  section: SDDView
  findings: SDDFinding[]
  /** The framework's declared findings-fix action, or null — the fix badges
   *  then render inert. */
  fixAction: SDDAction | null
  onOpen: (modelPath: string) => void
  onDispatch: (dispatch: SpecsDispatch, newSession: boolean) => void
}) {
  if (findings.length === 0) return null
  return (
    <section
      data-testid="specs-findings"
      data-view={section.id}
      className="flex flex-col gap-0.5"
    >
      <div className="flex items-center gap-1 px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        <AlertTriangle className="size-3" />
        {section.label}
      </div>
      {findings.map((finding, i) => (
        <FindingRow
          key={`${finding.kind}-${finding.file}-${i}`}
          finding={finding}
          fixAction={fixAction}
          onOpen={onOpen}
          onDispatch={onDispatch}
        />
      ))}
    </section>
  )
}
