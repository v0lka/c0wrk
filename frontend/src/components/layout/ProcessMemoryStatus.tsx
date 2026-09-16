import { Separator } from '@/components/ui/separator'
import { useProcessMemory } from '@/hooks/useProcessMemory'

/** Divisor for the agreed whole-mebibyte status-bar format. */
const BYTES_PER_MIB = 1024 * 1024

/**
 * Live resident-memory (RSS) indicator for the c0wrk desktop process itself.
 * Renders nothing — including its leading separator — until the first
 * successful sample arrives, so a hidden indicator never leaves a stray
 * separator at the right edge of the status bar. The visible label is the
 * agreed whole-mebibyte format ("RSS 1177 MiB", tabular digits so the width
 * stays stable between ticks); the tooltip carries the exact byte count.
 */
export function ProcessMemoryStatus() {
  const rssBytes = useProcessMemory()
  if (rssBytes == null || !Number.isFinite(rssBytes) || rssBytes < 0) return null

  const mib = Math.round(rssBytes / BYTES_PER_MIB)
  const label = `Process memory (RSS): ${rssBytes.toLocaleString('en-US')} bytes`

  return (
    <>
      <Separator orientation="vertical" className="mx-1 h-4" />
      <span className="shrink-0 text-xs text-muted-foreground" title={label}>
        RSS <span className="tabular-nums">{mib} MiB</span>
      </span>
    </>
  )
}
