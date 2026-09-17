// Source section — the paper's extracted source text with line-indexed
// anchor scrolling (E1).
//
// The text is rendered line by line so a resolved anchor maps to an exact,
// stable scroll target (`data-paper-line`). Rendering is monospace/preformatted
// — this is the SOURCE document the anchors point into, so fidelity beats
// styling. When the source is absent the section says so plainly.

import { useEffect, useMemo, useRef } from 'react'
import type { PaperAnchor } from '@/api/papers'
import { cn } from '@/lib/utils'
import { PaperAnchorList } from './PaperAnchorList'
import type { PaperArtifact } from './usePaperArtifacts'

/** A pending scroll request. The nonce makes a repeated click on the same
 *  anchor (same line) re-trigger the scroll effect. */
export interface PendingAnchor {
  line: number
  nonce: number
}

interface PaperSourceViewProps {
  artifact: PaperArtifact
  anchors: PaperAnchor[]
  pending: PendingAnchor | null
  missedIndex: number | null
  onAnchorSelect: (anchor: PaperAnchor, index: number) => void
}

function lineClass(line: string, highlighted: boolean): string {
  if (highlighted) return 'bg-highlight/20 text-foreground'
  if (/^\s{0,3}#{1,6}\s/.test(line)) return 'font-semibold text-foreground'
  return 'text-muted-foreground'
}

export function PaperSourceView({
  artifact,
  anchors,
  pending,
  missedIndex,
  onAnchorSelect,
}: PaperSourceViewProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const lines = useMemo(
    () => (artifact.content === '' ? [] : artifact.content.split('\n')),
    [artifact.content],
  )

  useEffect(() => {
    if (pending === null) return
    const el = containerRef.current?.querySelector(`[data-paper-line="${pending.line}"]`)
    if (el instanceof HTMLElement && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'center' })
    }
  }, [pending])

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {anchors.length > 0 && (
        <div data-testid="paper-source-anchors" className="shrink-0 border-b border-border px-2 py-1">
          <PaperAnchorList anchors={anchors} missedIndex={missedIndex} onSelect={onAnchorSelect} />
        </div>
      )}
      <div
        ref={containerRef}
        data-testid="paper-source"
        className="min-h-0 flex-1 overflow-auto custom-scrollbar py-1 font-mono text-[11px] leading-5"
      >
        {artifact.loading ? (
          <p className="px-2 py-3 text-center text-muted-foreground">Loading…</p>
        ) : artifact.error !== null ? (
          <p data-testid="paper-source-error" className="px-2 py-3 text-center text-destructive">
            {artifact.error}
          </p>
        ) : lines.length === 0 ? (
          <p data-testid="paper-source-empty" className="px-2 py-3 text-center text-muted-foreground">
            No source text captured for this paper yet.
          </p>
        ) : (
          lines.map((line, i) => (
            <div
              key={i}
              data-paper-line={i}
              className={cn('whitespace-pre-wrap break-words px-2', lineClass(line, pending?.line === i))}
            >
              {line === '' ? '\u00A0' : line}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
