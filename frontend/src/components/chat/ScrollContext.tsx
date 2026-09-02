import { createContext, useContext, useRef, useCallback, useMemo } from 'react'

type ScrollToStepFn = (stepId: string) => void
type ScrollToBookmarkFn = (key: string) => void

interface ScrollContextValue {
  scrollToStep: ScrollToStepFn | null
  setScrollToStep: (fn: ScrollToStepFn | null) => void
  scrollToBookmark: ScrollToBookmarkFn | null
  setScrollToBookmark: (fn: ScrollToBookmarkFn | null) => void
}

const ScrollContext = createContext<ScrollContextValue | null>(null)

export function ScrollProvider({ children }: { children: React.ReactNode }) {
  const stepFnRef = useRef<ScrollToStepFn | null>(null)
  const bookmarkFnRef = useRef<ScrollToBookmarkFn | null>(null)

  const setScrollToStep = useCallback((fn: ScrollToStepFn | null) => {
    stepFnRef.current = fn
  }, [])

  const scrollToStep = useCallback((stepId: string) => {
    stepFnRef.current?.(stepId)
  }, [])

  const setScrollToBookmark = useCallback((fn: ScrollToBookmarkFn | null) => {
    bookmarkFnRef.current = fn
  }, [])

  const scrollToBookmark = useCallback((key: string) => {
    bookmarkFnRef.current?.(key)
  }, [])

  const value = useMemo<ScrollContextValue>(
    () => ({ scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark }),
    [scrollToStep, setScrollToStep, scrollToBookmark, setScrollToBookmark],
  )

  return <ScrollContext value={value}>{children}</ScrollContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useScrollContext(): ScrollContextValue {
  const ctx = useContext(ScrollContext)
  if (!ctx) throw new Error('useScrollContext must be used within a ScrollProvider')
  return ctx
}
