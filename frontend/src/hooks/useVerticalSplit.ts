// Vertical (top/bottom) panel resize, expressed as a ratio of the container
// height rather than absolute pixels.
//
// The sidebar splits its remaining vertical space between the flat session
// list (top) and the workspace explorer (bottom). A ratio is the natural
// unit because the available height depends on window size and other fixed
// sections (header). Persisting a ratio keeps the chosen proportion stable
// across window resizes and reloads.
//
// Unlike the width-based useResize hook, this is self-contained: it binds
// its own document listeners on pointer down and reports the live ratio via
// onChange, clamped to [min, max].

import { useRef, useCallback, useEffect } from 'react'
import type { MouseEvent as ReactMouseEvent, KeyboardEvent as ReactKeyboardEvent } from 'react'

interface UseVerticalSplitOptions {
  /** Container element whose height the ratio is measured against. */
  containerRef: React.RefObject<HTMLElement | null>
  /** Current ratio in [0, 1]. */
  ratio: number
  /** Inclusive lower bound for the ratio. Default 0.1 (10%). */
  min?: number
  /** Inclusive upper bound for the ratio. Default 0.9 (90%). */
  max?: number
  onChange: (ratio: number) => void
}

interface UseVerticalSplitReturn {
  handleMouseDown: (e: ReactMouseEvent) => void
  handleKeyDown: (e: ReactKeyboardEvent) => void
}

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

export function useVerticalSplit({
  containerRef,
  ratio,
  min = 0.1,
  max = 0.9,
  onChange,
}: UseVerticalSplitOptions): UseVerticalSplitReturn {
  const dragging = useRef(false)
  const moveRef = useRef<((ev: globalThis.MouseEvent) => void) | null>(null)
  const upRef = useRef<(() => void) | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      if (moveRef.current) document.removeEventListener('mousemove', moveRef.current)
      if (upRef.current) document.removeEventListener('mouseup', upRef.current)
      dragging.current = false
      document.body.classList.remove('resize-dragging-row')
    }
  }, [])

  const handleMouseDown = useCallback((e: ReactMouseEvent) => {
    e.preventDefault()
    const container = containerRef.current
    if (!container) return
    dragging.current = true

    const onMouseMove = (ev: MouseEvent) => {
      if (!dragging.current) return
      const rect = container.getBoundingClientRect()
      if (rect.height <= 0) return
      const next = (ev.clientY - rect.top) / rect.height
      onChangeRef.current(clamp(next, min, max))
    }

    const onMouseUp = () => {
      dragging.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.classList.remove('resize-dragging-row')
      moveRef.current = null
      upRef.current = null
    }

    // Clean up any stale handlers before binding new ones.
    if (moveRef.current) document.removeEventListener('mousemove', moveRef.current)
    if (upRef.current) document.removeEventListener('mouseup', upRef.current)

    moveRef.current = onMouseMove
    upRef.current = onMouseUp
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.body.classList.add('resize-dragging-row')
  }, [containerRef, min, max])

  const handleKeyDown = useCallback((e: ReactKeyboardEvent) => {
    const step = e.shiftKey ? 0.1 : 0.02
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      onChangeRef.current(clamp(ratio - step, min, max))
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      onChangeRef.current(clamp(ratio + step, min, max))
    } else if (e.key === 'Home') {
      e.preventDefault()
      onChangeRef.current(min)
    } else if (e.key === 'End') {
      e.preventDefault()
      onChangeRef.current(max)
    }
  }, [ratio, min, max])

  return { handleMouseDown, handleKeyDown }
}
