// Pickaxe history search for the viewer's Git history page: the paginated
// backend -S/-G stream (searchGitHistory) UNIONED with client-side metadata
// matches (message/author/sha/date over the loaded window and the pickaxe
// pages), ordered newest-first. See specs/domains/frontend/git-history.md
// ("History search").

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { searchGitHistory, GIT_HISTORY_PAGE_SIZE } from '@/api/git'
import { isInvalidRegex } from '@/lib/pathFilter'
import type { GitHistoryCommit } from '@/types/models'

/** Pattern interpretation behind the search bar's text/regex toggle. */
export type HistorySearchMode = 'text' | 'regex'

/** Debounce window for the search input — a pickaxe scan walks diffs. */
const SEARCH_DEBOUNCE_MS = 300

/** The (pattern, mode) pair the RPCs actually run against once settled. */
interface EffectiveSearch {
  pattern: string
  mode: HistorySearchMode
}

interface UseGitHistorySearchReturn {
  searchPattern: string
  searchMode: HistorySearchMode
  setSearchPattern: (text: string) => void
  toggleSearchMode: () => void
  /** True once the debounced pattern is non-empty (search drives the list). */
  isSearching: boolean
  /** True while the current pattern's FIRST search page is in flight. */
  isSearchLoading: boolean
  /** True while an additional search page is in flight. */
  isSearchLoadingMore: boolean
  /** True when the pattern is a syntactically invalid regex (regex mode). */
  isInvalidSearch: boolean
  /** Union of pickaxe pages and metadata matches, deduped, newest first. */
  searchResults: GitHistoryCommit[]
  /** True when more pickaxe pages exist for the current pattern. */
  hasMore: boolean
  /** Latest search failure message (null while the search succeeds). */
  error: string | null
  /** Append the next pickaxe page (no-op while loading or exhausted). */
  loadMore: () => void
  /** Re-run the current pattern's first page (after a history mutation). */
  refresh: () => void
}

export function useGitHistorySearch(commits: GitHistoryCommit[]): UseGitHistorySearchReturn {
  const [searchPattern, setSearchPattern] = useState('')
  const [searchMode, setSearchMode] = useState<HistorySearchMode>('text')
  // Settled (pattern, mode): updated SEARCH_DEBOUNCE_MS after the input
  // or the toggle last changed; the RPCs and the matching read only this.
  const [effective, setEffective] = useState<EffectiveSearch>({ pattern: '', mode: 'text' })

  const toggleSearchMode = useCallback(() => {
    setSearchMode((m) => (m === 'text' ? 'regex' : 'text'))
  }, [])

  // The debounce timer arms on every input/mode change and is torn down by
  // the next change or unmount, so the effective value always lags the
  // input by at most SEARCH_DEBOUNCE_MS and never fires mid-typing.
  useEffect(() => {
    const timer = setTimeout(() => {
      setEffective({ pattern: searchPattern.trim(), mode: searchMode })
    }, SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [searchPattern, searchMode])

  const trimmedPattern = effective.pattern
  const isSearching = trimmedPattern !== ''
  // isInvalidRegex's glob branch is always false — for text mode that is
  // exactly the contract (nothing to validate).
  const isInvalidSearch = isInvalidRegex(trimmedPattern, effective.mode === 'regex' ? 'regex' : 'glob')

  // ── Pickaxe stream: paginated, epoch-guarded ────────────────────────
  const [pickaxeCommits, setPickaxeCommits] = useState<GitHistoryCommit[]>([])
  const [hasMore, setHasMore] = useState(false)
  const [isSearchLoading, setIsSearchLoading] = useState(false)
  const [isSearchLoadingMore, setIsSearchLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Offset of the next pickaxe page; a ref so loadMore stays stable.
  const nextSkipRef = useRef(0)
  // Guards overlapping loadMore calls (scroll edge + button, rapid fires).
  const loadingMoreRef = useRef(false)
  // Epoch token: every new first-page load bumps it, so an in-flight page
  // of the previous pattern cannot land into the new result set.
  const epochRef = useRef(0)

  const loadFirstPage = useCallback(async () => {
    epochRef.current += 1
    const epoch = epochRef.current
    loadingMoreRef.current = false
    // Both loading flags belong to the pattern being replaced: a first page
    // or a loadMore of the previous pattern may still be in flight, and its
    // finally block deliberately skips the reset for a stale epoch — so
    // clear them here, or a stuck "Searching…" indicator and a permanently
    // disabled "Load more" would follow the new pattern.
    setIsSearchLoading(false)
    setIsSearchLoadingMore(false)
    // The new pattern invalidates the previous one's pagination state
    // immediately — a stale hasMore/next_skip must never survive into the
    // window where the fresh first page is still in flight.
    setPickaxeCommits([])
    setHasMore(false)
    nextSkipRef.current = 0
    if (trimmedPattern === '' || isInvalidSearch) {
      setError(null)
      return
    }
    setIsSearchLoading(true)
    setError(null)
    try {
      const page = await searchGitHistory(
        trimmedPattern,
        effective.mode === 'regex',
        GIT_HISTORY_PAGE_SIZE,
        0,
      )
      if (epoch !== epochRef.current) return
      setPickaxeCommits(page.commits)
      nextSkipRef.current = page.next_skip
      setHasMore(page.has_more)
    } catch (err) {
      if (epoch !== epochRef.current) return
      setPickaxeCommits([])
      setHasMore(false)
      setError(err instanceof Error ? err.message : 'Failed to search history')
    } finally {
      if (epoch === epochRef.current) setIsSearchLoading(false)
    }
  }, [trimmedPattern, effective.mode, isInvalidSearch])

  useEffect(() => {
    void loadFirstPage()
  }, [loadFirstPage])

  const loadMore = useCallback(() => {
    // Blocked while the first page is in flight: nextSkipRef belongs to
    // the settled pattern only.
    if (loadingMoreRef.current || isSearchLoading || !hasMore || trimmedPattern === '' || isInvalidSearch) return
    loadingMoreRef.current = true
    setIsSearchLoadingMore(true)
    // A retry starts from a clean slate: the previous attempt's failure
    // must not outlive the page it belongs to.
    setError(null)
    const epoch = epochRef.current
    void searchGitHistory(trimmedPattern, effective.mode === 'regex', GIT_HISTORY_PAGE_SIZE, nextSkipRef.current)
      .then((page) => {
        if (epoch !== epochRef.current) return
        // Append — never replace — so loaded results keep their identity.
        setPickaxeCommits((prev) => [...prev, ...page.commits])
        nextSkipRef.current = page.next_skip
        setHasMore(page.has_more)
      })
      .catch((err: unknown) => {
        if (epoch !== epochRef.current) return
        setError(err instanceof Error ? err.message : 'Failed to load more results')
      })
      .finally(() => {
        if (epoch === epochRef.current) {
          setIsSearchLoadingMore(false)
          loadingMoreRef.current = false
        }
      })
  }, [hasMore, isSearchLoading, trimmedPattern, effective.mode, isInvalidSearch])

  // ── Metadata matcher over the known commits ─────────────────────────
  const metaMatcher = useMemo<(c: GitHistoryCommit) => boolean>(() => {
    if (trimmedPattern === '' || isInvalidSearch) return () => false
    if (effective.mode === 'regex') {
      const re = new RegExp(trimmedPattern, 'i')
      return (c) => re.test(c.message) || re.test(c.author) || re.test(c.sha) || re.test(c.date)
    }
    const needle = trimmedPattern.toLowerCase()
    return (c) =>
      c.message.toLowerCase().includes(needle) ||
      c.author.toLowerCase().includes(needle) ||
      c.sha.toLowerCase().includes(needle) ||
      c.date.toLowerCase().includes(needle)
  }, [trimmedPattern, effective.mode, isInvalidSearch])

  const searchResults = useMemo(() => {
    if (!isSearching || isInvalidSearch) return []
    const picked = new Set(pickaxeCommits.map((c) => c.sha))
    const results: GitHistoryCommit[] = []
    const seen = new Set<string>()
    for (const c of commits) {
      if (metaMatcher(c) || picked.has(c.sha)) {
        results.push(c)
        seen.add(c.sha)
      }
    }
    // Pickaxe matches beyond the loaded window — strictly older than
    // anything in it, so appending keeps the list newest-first.
    for (const c of pickaxeCommits) {
      if (!seen.has(c.sha)) {
        results.push(c)
        seen.add(c.sha)
      }
    }
    return results
  }, [isSearching, isInvalidSearch, commits, pickaxeCommits, metaMatcher])

  // Re-run the current pattern's first page. A history mutation (tag, reset)
  // rewrites the very commits the pickaxe pages describe, so the page's
  // `onAfterMutation` refreshes this stream alongside the window.
  const refresh = useCallback(() => {
    void loadFirstPage()
  }, [loadFirstPage])

  return {
    searchPattern,
    searchMode,
    setSearchPattern,
    toggleSearchMode,
    isSearching,
    isSearchLoading,
    isSearchLoadingMore,
    isInvalidSearch,
    searchResults,
    hasMore,
    error,
    loadMore,
    refresh,
  }
}
