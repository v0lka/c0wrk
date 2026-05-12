// Fuzzy subsequence matching with scoring for autocomplete filtering.

interface FuzzyResult {
  match: boolean
  score: number
}

/**
 * Test if `query` is a fuzzy subsequence of `target`.
 * Scoring: consecutive matches get bonus, word-boundary matches get bonus.
 */
export function fuzzyMatch(query: string, target: string): FuzzyResult {
  if (query.length === 0) return { match: true, score: 0 }
  if (query.length > target.length) return { match: false, score: 0 }

  const queryLower = query.toLowerCase()
  const targetLower = target.toLowerCase()

  let qi = 0
  let score = 0
  let consecutive = 0
  let lastMatchIdx = -2

  for (let ti = 0; ti < targetLower.length && qi < queryLower.length; ti++) {
    if (targetLower[ti] === queryLower[qi]) {
      qi++
      // Consecutive match bonus
      if (ti === lastMatchIdx + 1) {
        consecutive++
        score += consecutive * 2
      } else {
        consecutive = 0
        score += 1
      }
      // Word boundary bonus (after /, -, _, . or start)
      if (ti === 0 || '/\\-_.'.includes(targetLower[ti - 1] ?? '')) {
        score += 3
      }
      lastMatchIdx = ti
    }
  }

  if (qi < queryLower.length) return { match: false, score: 0 }

  // Shorter targets score higher (tighter match)
  score += Math.max(0, 20 - target.length)

  return { match: true, score }
}

/**
 * Filter and sort items by fuzzy match against a text extractor.
 * Returns matched items sorted by score (highest first), capped at `limit`.
 */
export function fuzzyFilter<T>(
  query: string,
  items: T[],
  getText: (item: T) => string,
  limit = 50,
): T[] {
  if (query.length === 0) return items.slice(0, limit)

  const scored: Array<{ item: T; score: number }> = []
  for (const item of items) {
    const result = fuzzyMatch(query, getText(item))
    if (result.match) {
      scored.push({ item, score: result.score })
    }
  }

  scored.sort((a, b) => b.score - a.score)
  return scored.slice(0, limit).map((s) => s.item)
}
