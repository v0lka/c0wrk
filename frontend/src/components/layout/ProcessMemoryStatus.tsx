import { useProcessMemory } from '@/hooks/useProcessMemory'

/** Divisor for the agreed whole-megabyte status-bar format. */
const BYTES_PER_MB = 1024 * 1024

/**
 * Live resident-memory (RSS) indicator for the c0wrk desktop process itself.
 * Renders nothing until the first successful sample arrives. The visible
 * label is the agreed whole-megabyte format ("RSS 1177 MB", tabular digits so
 * the width stays stable between ticks); the tooltip carries the exact byte
 * count.
 */
export function ProcessMemoryStatus() {
  const rssBytes = useProcessMemory()
  if (rssBytes == null || !Number.isFinite(rssBytes) || rssBytes < 0) return null

  const mb = Math.round(rssBytes / BYTES_PER_MB)
  const label = `Process memory (RSS): ${rssBytes.toLocaleString('en-US')} bytes`

  return (
    <span className="shrink-0 text-xs text-muted-foreground" title={label} aria-label={label}>
      RSS <span className="tabular-nums">{mb} MB</span>
    </span>
  )
}
