// Source section — the paper's source document with anchor navigation (E1)
// and three sub-views:
//   Original HTML — the fetched paper.html, sanitized and rendered in place
//                   (PaperHtmlView); anchors resolve against its element ids
//                   and captions first.
//   Extracted     — source.md rendered as real markdown, block-mapped to its
//                   source lines (PaperSourceExtracted); the sub-view an
//                   HTML-miss anchor falls back to.
//   Raw           — source.md one line per row, exactly as the workspace
//                   rendered it before paper.html existed (PaperSourceRaw).
//
// The Original HTML sub-view is the default whenever paper.html exists; the
// Extracted one stands alone when it does not (the pre-paper.html behavior),
// and a stale 'html' mode falls through to it. The anchor bar, the honest
// "not found" marking, and the section actions (fetch the HTML original /
// open the arXiv abs page) are shared by all three sub-views; the chosen mode
// is workspace state, so it survives section switches for the session view.

import type { PaperAnchor, PaperRecord } from '@/api/papers'
import { cn } from '@/lib/utils'
import { PaperAnchorList } from './PaperAnchorList'
import { PaperHtmlView, type PendingHtmlAnchor } from './PaperHtmlView'
import { PaperSourceActions } from './PaperSourceActions'
import { PaperSourceExtracted } from './PaperSourceExtracted'
import { PaperSourceRaw } from './PaperSourceRaw'
import type { PaperArtifact } from './usePaperArtifacts'

/** A pending scroll request. The nonce makes a repeated click on the same
 *  anchor (same line) re-trigger the scroll effect. */
export interface PendingAnchor {
  line: number
  nonce: number
}

/** Which source sub-view is showing. */
export type PaperSourceViewMode = 'html' | 'text' | 'raw'

interface PaperSourceViewProps {
  paper: PaperRecord
  artifact: PaperArtifact
  /** The fetched paper.html artifact (missing when the paper has none). */
  html: PaperArtifact
  /** Absolute path of the paper.html document (img src resolution base). */
  htmlBaseFilePath?: string | null
  /** Absolute path of source.md (relative image resolution base). */
  sourceBaseFilePath?: string | null
  anchors: PaperAnchor[]
  pending: PendingAnchor | null
  /** A resolved paper.html anchor to reveal in the rendered sub-view. */
  pendingHtml: PendingHtmlAnchor | null
  /** The active sub-view ('html' is advisory — ignored when paper.html is
   *  absent, so the extracted view stands alone as before). */
  mode: PaperSourceViewMode
  onModeChange: (mode: PaperSourceViewMode) => void
  missedIndex: number | null
  onAnchorSelect: (anchor: PaperAnchor, index: number) => void
}

function subViewButtonClass(active: boolean): string {
  return cn(
    'rounded px-1.5 py-0.5 text-[10px] transition-colors',
    active ? 'bg-background text-foreground' : 'text-muted-foreground hover:bg-muted/40 hover:text-foreground',
  )
}

export function PaperSourceView({
  paper,
  artifact,
  html,
  htmlBaseFilePath,
  sourceBaseFilePath,
  anchors,
  pending,
  pendingHtml,
  mode,
  onModeChange,
  missedIndex,
  onAnchorSelect,
}: PaperSourceViewProps) {
  const htmlReady = html.content !== '' && html.error === null
  const sourceReady = artifact.content !== ''
  // 'html' is advisory: without paper.html it falls through to the extracted
  // view, which stands alone exactly as it did before paper.html existed.
  const effectiveMode = htmlReady || mode !== 'html' ? mode : 'text'
  const showHtml = effectiveMode === 'html'
  const showRaw = effectiveMode === 'raw'
  const viewCount = (htmlReady ? 1 : 0) + (sourceReady ? 2 : 0)

  const subViewToggle = viewCount > 1 && (
    <div
      data-testid="paper-source-subviews"
      className={cn('flex shrink-0 items-center gap-0.5 rounded border border-border p-0.5', anchors.length > 0 && 'ml-auto')}
    >
      {htmlReady && (
        <button
          type="button"
          data-testid="paper-source-subview-html"
          data-active={showHtml}
          aria-pressed={showHtml}
          onClick={() => onModeChange('html')}
          className={subViewButtonClass(showHtml)}
        >
          Original HTML
        </button>
      )}
      {sourceReady && (
        <>
          <button
            type="button"
            data-testid="paper-source-subview-text"
            data-active={effectiveMode === 'text'}
            aria-pressed={effectiveMode === 'text'}
            onClick={() => onModeChange('text')}
            className={subViewButtonClass(effectiveMode === 'text')}
          >
            Extracted
          </button>
          <button
            type="button"
            data-testid="paper-source-subview-raw"
            data-active={showRaw}
            aria-pressed={showRaw}
            onClick={() => onModeChange('raw')}
            className={subViewButtonClass(showRaw)}
          >
            Raw
          </button>
        </>
      )}
    </div>
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {anchors.length > 0 ? (
        <div
          data-testid="paper-source-anchors"
          className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-border px-2 py-1"
        >
          <PaperAnchorList anchors={anchors} missedIndex={missedIndex} onSelect={onAnchorSelect} />
          {subViewToggle}
        </div>
      ) : subViewToggle !== false ? (
        <div className="flex shrink-0 items-center justify-end border-b border-border px-2 py-1">
          {subViewToggle}
        </div>
      ) : null}
      <PaperSourceActions paper={paper} hasHtml={htmlReady} onFetched={() => onModeChange('html')} />
      {showHtml ? (
        <div
          data-testid="paper-source-html"
          className="min-h-0 flex-1 overflow-auto custom-scrollbar"
        >
          {html.loading ? (
            <p className="px-2 py-3 text-center text-muted-foreground">Loading…</p>
          ) : (
            <PaperHtmlView
              content={html.content}
              baseFilePath={htmlBaseFilePath ?? null}
              pendingAnchor={pendingHtml}
              className="px-3 pb-6"
            />
          )}
        </div>
      ) : showRaw ? (
        <PaperSourceRaw artifact={artifact} pending={pending} />
      ) : (
        <PaperSourceExtracted artifact={artifact} pending={pending} baseFilePath={sourceBaseFilePath} />
      )}
    </div>
  )
}
