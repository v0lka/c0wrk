import { useEffect } from 'react'
import { HISTORY_PAGE_SIZE } from './useGitHistory'

/**
 * Target number of matched commits to accumulate while a file filter is
 * active. The hook auto-loads pages until this many matches are found,
 * the log is exhausted, or the safety cap is reached.
 */
const FILTER_AUTOFILL_TARGET = HISTORY_PAGE_SIZE

/**
 * Safety cap on total commits auto-loaded while filtering. Prevents
 * fetching the entire repo for rare filters. After this cap, "Load more"
 * appears for manual continuation.
 */
const FILTER_AUTOFILL_MAX_LOADED = 200

export { FILTER_AUTOFILL_TARGET, FILTER_AUTOFILL_MAX_LOADED }

interface UseGitHistoryAutofillParams {
  isFiltering: boolean
  isInvalidFilter: boolean
  isResolvingFiles: boolean
  filteredCount: number
  loadedCount: number
  /** True when every loaded commit has its changed files cached. */
  allFilesResolved: boolean
  hasMore: boolean
  isLoadingMore: boolean
  loadMore: () => void
}

/**
 * While a file filter is active, auto-loads more history pages until
 * enough matched commits are accumulated (`FILTER_AUTOFILL_TARGET`), the
 * log is exhausted (`!hasMore`), or the safety cap is reached
 * (`FILTER_AUTOFILL_MAX_LOADED`). Returns `isAutofilling` so the caller
 * can show a loading indicator instead of the "Load more" button.
 *
 * The `allFilesResolved` guard ensures we don't fire the next page load
 * before the filter hook has had a chance to fetch changed files for the
 * newly loaded commits — without it, a 1-frame gap between `loadMore()`
 * completing and the filter effect starting would cause a premature
 * auto-load that skips the just-loaded page before its files are tested.
 */
export function useGitHistoryAutofill({
  isFiltering,
  isInvalidFilter,
  isResolvingFiles,
  filteredCount,
  loadedCount,
  allFilesResolved,
  hasMore,
  isLoadingMore,
  loadMore,
}: UseGitHistoryAutofillParams): boolean {
  const isAutofilling =
    isFiltering &&
    !isInvalidFilter &&
    !isResolvingFiles &&
    !isLoadingMore &&
    hasMore &&
    filteredCount < FILTER_AUTOFILL_TARGET &&
    loadedCount < FILTER_AUTOFILL_MAX_LOADED &&
    allFilesResolved

  useEffect(() => {
    if (!isAutofilling) return
    void loadMore()
  }, [isAutofilling, loadMore])

  return isAutofilling
}
