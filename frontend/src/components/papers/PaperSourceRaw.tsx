// The Raw sub-view of the Source section — the extracted source text exactly
// as the workspace rendered it before the markdown Extracted view existed:
// one monospace line per source line, each carrying data-paper-line so a
// resolved anchor scrolls to the EXACT line (not its containing block).
// Behavior-frozen by design; the Extracted sub-view is the new default.

import { useEffect, useMemo, useRef } from 'react'
import { cn } from '@/lib/utils'
import type { PendingAnchor } from './PaperSourceView'
import type { PaperArtifact } from './usePaperArtifacts'

function lineClass(line: string, highlighted: boolean): string {
  if (highlighted) return 'bg-highlight/20 text-foreground'
  if (/^\s{0,3}#{1,6}\s/.test(line)) return 'font-semibold text-foreground'
  return 'text-muted-foreground'
}

interface PaperSourceRawProps {
  artifact: PaperArtifact
  pending: PendingAnchor | null
}

export function PaperSourceRaw({ artifact, pending }: PaperSourceRawProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const lines = useMemo(
    () => (artifact.content === '' ? [] : artifact.content.split('\n')),
    [artifact.content],
  )

  // A pending anchor scrolls to the exact line element.
  useEffect(() => {
    if (pending === null) return
    const el = containerRef.current?.querySelector(`[data-paper-line="${pending.line}"]`)
    if (el instanceof HTMLElement && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'center' })
    }
  }, [pending])

  return (
    <div
      ref={containerRef}
      data-testid="paper-source-raw"
      className="min-h-0 flex-1 overflow-auto custom-scrollbar py-1 font-mono text-[11px] leading-5"
    >
      {artifact.loading ? (
        <p className="px-2 py-3 text-center text-muted-foreground">Loading…</p>
      ) : artifact.error !== null ? (
        <p data-testid="paper-source-raw-error" className="px-2 py-3 text-center text-destructive">
          {artifact.error}
        </p>
      ) : lines.length === 0 ? (
        <p data-testid="paper-source-raw-empty" className="px-2 py-3 text-center text-muted-foreground">
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
  )
}
