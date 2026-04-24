import { createContext, useContext, useRef, useCallback, useMemo } from 'react'

type ScrollToStepFn = (stepId: string) => void

interface ScrollContextValue {
  scrollToStep: ScrollToStepFn | null
  setScrollToStep: (fn: ScrollToStepFn | null) => void
}

const ScrollContext = createContext<ScrollContextValue | null>(null)

export function ScrollProvider({ children }: { children: React.ReactNode }) {
  const fnRef = useRef<ScrollToStepFn | null>(null)

  const setScrollToStep = useCallback((fn: ScrollToStepFn | null) => {
    fnRef.current = fn
  }, [])

  const scrollToStep = useCallback((stepId: string) => {
    fnRef.current?.(stepId)
  }, [])

  const value = useMemo<ScrollContextValue>(
    () => ({ scrollToStep, setScrollToStep }),
    [scrollToStep, setScrollToStep],
  )

  return <ScrollContext value={value}>{children}</ScrollContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useScrollContext(): ScrollContextValue {
  const ctx = useContext(ScrollContext)
  if (!ctx) throw new Error('useScrollContext must be used within a ScrollProvider')
  return ctx
}
