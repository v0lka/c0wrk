/**
 * Pure display formatters for the ChatGPT subscription surfaces (Settings →
 * ChatGPT provider). Kept out of the component files so fast refresh keeps
 * working (react-refresh/only-export-components) and the formatting stays
 * directly unit-testable.
 */

/** Format an RFC3339 token expiry for display. Unparseable values are shown
 *  verbatim rather than silently dropped — the raw string is still more
 *  information than an empty field. */
export function formatAuthExpiry(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** Compact token-count label for a preset model's context window (the oauth
 *  checklist badge), e.g. 176K / 272K / 1M. Zero/absent metadata renders as
 *  an empty string and the caller draws no badge. */
export function formatContextWindow(tokens: number): string {
  if (tokens <= 0) return ''
  return new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(tokens)
}
