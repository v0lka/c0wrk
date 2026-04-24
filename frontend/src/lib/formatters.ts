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
  if (diffMs < HOUR) return `${Math.floor(diffMs / MIN)}m ago`
  if (diffMs < DAY) return `${Math.floor(diffMs / HOUR)}h ago`
  if (diffMs < 7 * DAY) return `${Math.floor(diffMs / DAY)}d ago`

  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
