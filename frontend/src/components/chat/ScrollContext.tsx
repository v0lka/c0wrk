import { createContext, useContext, useRef, useCallback, useMemo } from 'react'

type ScrollToStepFn = (stepId: string) => void
type ScrollToBookmarkFn = (key: string) => void
type ScrollViewportFn = () => HTMLElement | null

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
}

const ScrollContext = createContext<ScrollContextValue | null>(null)

export function ScrollProvider({ children }: { children: React.ReactNode }) {
  const stepFnRef = useRef<ScrollToStepFn | null>(null)
  const bookmarkFnRef = useRef<ScrollToBookmarkFn | null>(null)
  const viewportFnRef = useRef<ScrollViewportFn | null>(null)

  const setScrollToStep = useCallback((fn: ScrollToStepFn | null) => {
    stepFnRef.current = fn
  }, [])

  const setScrollToBookmark = useCallback((fn: ScrollToBookmarkFn | null) => {
    bookmarkFnRef.current = fn
  }, [])

  const setScrollViewport = useCallback((fn: ScrollViewportFn | null) => {
    viewportFnRef.current = fn
  }, [])

  const getScrollViewport = useCallback<ScrollViewportFn>(
    () => viewportFnRef.current?.() ?? null,
    [],
  )

  const scrollToStep = useCallback((stepId: string) => {
    stepFnRef.current?.(stepId)
  }, [])

  const scrollToBookmark = useCallback((key: string) => {
    bookmarkFnRef.current?.(key)
  }, [])

  const value = useMemo<ScrollContextValue>(
    () => ({ scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark, getScrollViewport, setScrollViewport }),
    [scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark, getScrollViewport, setScrollViewport],
  )

  return <ScrollContext value={value}>{children}</ScrollContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useScrollContext(): ScrollContextValue {
  const ctx = useContext(ScrollContext)
  if (!ctx) throw new Error('useScrollContext must be used within a ScrollProvider')
  return ctx
}

// eslint-disable-next-line react-refresh/only-export-components
export function useChatScrollViewport(): ScrollViewportFn {
  const ctx = useContext(ScrollContext)
  return ctx?.getScrollViewport ?? ((): null => null)
}
