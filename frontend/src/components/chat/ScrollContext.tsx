import { createContext, useContext, useRef, useCallback, useMemo } from 'react'

type ScrollToStepFn = (stepId: string) => void
type ScrollToBookmarkFn = (key: string) => void
type ScrollViewportFn = () => HTMLElement | null
type IsAtBottomFn = () => boolean

interface ScrollContextValue {
  scrollToStep: ScrollToStepFn | null
  setScrollToStep: (fn: ScrollToStepFn | null) => void
  scrollToBookmark: ScrollToBookmarkFn | null
  setScrollToBookmark: (fn: ScrollToBookmarkFn | null) => void
  /**
   * Resolves the chat transcript's scroll viewport element (ChatScrollManager's
   * scroll container), or null when the manager is not mounted — the accessor
   * the collapse-reposition path in {@link CollapsibleBlock} uses. Kept as a
   * getter (not an element) so a session switch's remount — which swaps the
   * manager and its viewport node — can never hand out a detached element.
   */
  getScrollViewport: ScrollViewportFn
  setScrollViewport: (fn: ScrollViewportFn | null) => void
  /**
   * Whether the chat viewport is pinned to the bottom per ChatScrollManager's
   * stick-to-bottom tracking. A getter (not a boolean) so a consumer reads the
   * state at the moment it acts — CollapsibleBlock's collapse effect gates its
   * reposition on it — instead of a stale render-time snapshot. Unregistered
   * (outside the transcript) it resolves to false, i.e. "not at bottom":
   * consumers keep their default behavior rather than suppress it.
   */
  getIsAtBottom: IsAtBottomFn
  /** Registers the at-bottom resolver (ChatScrollManager's `isAtBottomRef`). */
  setIsAtBottom: (fn: IsAtBottomFn | null) => void
}

const ScrollContext = createContext<ScrollContextValue | null>(null)

export function ScrollProvider({ children }: { children: React.ReactNode }) {
  const stepFnRef = useRef<ScrollToStepFn | null>(null)
  const bookmarkFnRef = useRef<ScrollToBookmarkFn | null>(null)
  const viewportFnRef = useRef<ScrollViewportFn | null>(null)
  const atBottomFnRef = useRef<IsAtBottomFn | null>(null)

  const setScrollToStep = useCallback((fn: ScrollToStepFn | null) => {
    stepFnRef.current = fn
  }, [])

  const setScrollToBookmark = useCallback((fn: ScrollToBookmarkFn | null) => {
    bookmarkFnRef.current = fn
  }, [])

  const setScrollViewport = useCallback((fn: ScrollViewportFn | null) => {
    viewportFnRef.current = fn
  }, [])

  const setIsAtBottom = useCallback((fn: IsAtBottomFn | null) => {
    atBottomFnRef.current = fn
  }, [])

  const getScrollViewport = useCallback<ScrollViewportFn>(
    () => viewportFnRef.current?.() ?? null,
    [],
  )

  const getIsAtBottom = useCallback<IsAtBottomFn>(
    () => atBottomFnRef.current?.() ?? false,
    [],
  )

  const scrollToStep = useCallback((stepId: string) => {
    stepFnRef.current?.(stepId)
  }, [])

  const scrollToBookmark = useCallback((key: string) => {
    bookmarkFnRef.current?.(key)
  }, [])

  const value = useMemo<ScrollContextValue>(
    () => ({ scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark, getScrollViewport, setScrollViewport, getIsAtBottom, setIsAtBottom }),
    [scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark, getScrollViewport, setScrollViewport, getIsAtBottom, setIsAtBottom],
  )

  return <ScrollContext value={value}>{children}</ScrollContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useScrollContext(): ScrollContextValue {
  const ctx = useContext(ScrollContext)
  if (!ctx) throw new Error('useScrollContext must be used within a ScrollProvider')
  return ctx
}

// Hoisted fallbacks: a fresh closure per call would change identity on every
// render and re-run every consumer effect that lists the hook's result in its
// dependency array.
const NULL_SCROLL_VIEWPORT: ScrollViewportFn = (): null => null
const NOT_AT_BOTTOM: IsAtBottomFn = () => false

// eslint-disable-next-line react-refresh/only-export-components
export function useChatScrollViewport(): ScrollViewportFn {
  const ctx = useContext(ScrollContext)
  return ctx?.getScrollViewport ?? NULL_SCROLL_VIEWPORT
}

// eslint-disable-next-line react-refresh/only-export-components
export function useChatScrollAtBottom(): IsAtBottomFn {
  const ctx = useContext(ScrollContext)
  return ctx?.getIsAtBottom ?? NOT_AT_BOTTOM
}
