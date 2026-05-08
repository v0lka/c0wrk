import { useRef, useCallback, useEffect, type MouseEvent as ReactMouseEvent, type KeyboardEvent as ReactKeyboardEvent } from 'react'

interface UseResizeOptions {
  initialWidth: number
  min: number
  max: number
  /** Set to -1 for right-side panels where drag-right should shrink. Default: 1. */
  direction?: 1 | -1
  onChange: (width: number) => void
}

interface UseResizeReturn {
  handleMouseDown: (e: ReactMouseEvent) => void
  handleKeyDown: (e: ReactKeyboardEvent) => void
}

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

export function useResize({ initialWidth, min, max, direction = 1, onChange }: UseResizeOptions): UseResizeReturn {
  const dragging = useRef(false)
  const startX = useRef(0)
  const startWidth = useRef(initialWidth)
  const moveRef = useRef<((ev: globalThis.MouseEvent) => void) | null>(null)
  const upRef = useRef<(() => void) | null>(null)

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (moveRef.current) document.removeEventListener('mousemove', moveRef.current)
      if (upRef.current) document.removeEventListener('mouseup', upRef.current)
      dragging.current = false
      document.body.classList.remove('resize-dragging')
    }
  }, [])

  const handleMouseDown = useCallback((e: ReactMouseEvent) => {
    e.preventDefault()
    dragging.current = true
    startX.current = e.clientX
    startWidth.current = initialWidth

    const onMouseMove = (ev: MouseEvent) => {
      if (!dragging.current) return
      const delta = (ev.clientX - startX.current) * direction
      onChange(clamp(startWidth.current + delta, min, max))
    }

    const onMouseUp = () => {
      dragging.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.classList.remove('resize-dragging')
      moveRef.current = null
      upRef.current = null
    }

    // Clean up any stale handlers
    if (moveRef.current) document.removeEventListener('mousemove', moveRef.current)
    if (upRef.current) document.removeEventListener('mouseup', upRef.current)

    moveRef.current = onMouseMove
    upRef.current = onMouseUp
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.body.classList.add('resize-dragging')
  }, [initialWidth, min, max, direction, onChange])

  const handleKeyDown = useCallback((e: ReactKeyboardEvent) => {
    const step = e.shiftKey ? 50 : 10
    if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault()
      onChange(clamp(initialWidth - step * direction, min, max))
    } else if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
      e.preventDefault()
      onChange(clamp(initialWidth + step * direction, min, max))
    }
  }, [initialWidth, min, max, direction, onChange])

  return { handleMouseDown, handleKeyDown }
}
