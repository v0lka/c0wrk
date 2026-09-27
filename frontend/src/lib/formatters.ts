export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '0ms'
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem > 0 ? `${m}m${rem}s` : `${m}m`
}

export function formatTokenCount(count: number): string {
  if (!Number.isFinite(count) || count < 0) return '0'
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`
  if (count >= 1_000) return `${(count / 1_000).toFixed(1)}K`
  return count.toString()
}

export function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr)
  const now = Date.now()
  const diffMs = now - date.getTime()

  if (diffMs < 0 || !Number.isFinite(diffMs)) return 'just now'

  const MIN = 60_000
  const HOUR = 60 * MIN
  const DAY = 24 * HOUR

  if (diffMs < MIN) return 'just now'
  if (diffMs < HOUR) return `${Math.floor(diffMs / MIN)}m`
  if (diffMs < DAY) return `${Math.floor(diffMs / HOUR)}h`
  if (diffMs < 7 * DAY) return `${Math.floor(diffMs / DAY)}d`

  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

/** Display a release version with one `v` prefix while preserving channels such as `dev`. */
export function formatVersion(version: string): string {
  if (version.startsWith('v') || !/^\d/.test(version)) return version
  return `v${version}`
}

/** Human-readable byte size using binary units (B/KB/MB/GB). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** Compact countdown readout of a remaining-seconds figure: `41m 40s` under an
 *  hour, `1h 05m` above it (the minutes drop their seconds so the string stays
 *  narrow in a status line). Negative and non-finite inputs read as zero. */
export function formatIdleCountdown(seconds: number): string {
  if (!Number.isFinite(seconds)) return '0m 00s'
  const s = Math.max(0, Math.floor(seconds))
  if (s >= 3600) {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    return `${h}h ${String(m).padStart(2, '0')}m`
  }
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
}
