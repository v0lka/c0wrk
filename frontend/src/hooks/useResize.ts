import { useRef, useCallback, useEffect, type MouseEvent as ReactMouseEvent, type KeyboardEvent as ReactKeyboardEvent } from 'react'

interface UseResizeOptions {
  initialWidth: number
  min: number
  max: number
  /**
   * Sign mapping drag-axis movement to panel size: with 1 (default) dragging
   * in the positive axis direction grows the measured panel (left sidebar:
   * drag-right grows it); with -1 it shrinks (right-side panel: drag-right
   * shrinks it; bottom panel: drag-down shrinks it). Pick the sign that
   * makes the divider FOLLOW the pointer/keys.
   */
  direction?: 1 | -1
  /**
   * Which axis the handle drags along: 'x' (horizontal handle, resize by
   * width — the default) or 'y' (vertical handle, resize by height). The
   * keyboard mapping follows the axis: on 'x' the Left/Right pair and on
   * 'y' the Up/Down pair move the divider along the drag axis — `direction`
   * decides whether that movement grows or shrinks the measured panel.
   */
  axis?: 'x' | 'y'
  onChange: (width: number) => void
}

interface UseResizeReturn {
  handleMouseDown: (e: ReactMouseEvent) => void
  handleKeyDown: (e: ReactKeyboardEvent) => void
}

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

export function useResize({ initialWidth, min, max, direction = 1, axis = 'x', onChange }: UseResizeOptions): UseResizeReturn {
  const dragging = useRef(false)
  const startX = useRef(0)
  const startWidth = useRef(initialWidth)
  const moveRef = useRef<((ev: globalThis.MouseEvent) => void) | null>(null)
  const upRef = useRef<(() => void) | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  // The row-variant drag cursor class (see index.css): the column variant is
  // the default 'resize-dragging'.
  const dragClass = axis === 'y' ? 'resize-dragging-row' : 'resize-dragging'

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (moveRef.current) document.removeEventListener('mousemove', moveRef.current)
      if (upRef.current) document.removeEventListener('mouseup', upRef.current)
      dragging.current = false
      document.body.classList.remove(dragClass)
    }
  }, [dragClass])

  const handleMouseDown = useCallback((e: ReactMouseEvent) => {
    e.preventDefault()
    dragging.current = true
    startX.current = axis === 'y' ? e.clientY : e.clientX
    startWidth.current = initialWidth

    const onMouseMove = (ev: MouseEvent) => {
      if (!dragging.current) return
      const delta = ((axis === 'y' ? ev.clientY : ev.clientX) - startX.current) * direction
      onChangeRef.current(clamp(startWidth.current + delta, min, max))
    }

    const onMouseUp = () => {
      dragging.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.classList.remove(dragClass)
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
    document.body.classList.add(dragClass)
  }, [initialWidth, min, max, direction, axis, dragClass])

  const handleKeyDown = useCallback((e: ReactKeyboardEvent) => {
    const step = e.shiftKey ? 50 : 10
    // Axis-aware keys: the Left/Up pair moves the divider up/left and the
    // Right/Down pair moves it down/right along the drag axis; `direction`
    // decides whether that grows or shrinks the measured panel — either
    // way the divider follows the input, mirroring the drag.
    const dividerUpKey = axis === 'y' ? 'ArrowUp' : 'ArrowLeft'
    const dividerDownKey = axis === 'y' ? 'ArrowDown' : 'ArrowRight'
    if (e.key === dividerUpKey || (axis === 'x' && e.key === 'ArrowUp')) {
      e.preventDefault()
      onChangeRef.current(clamp(initialWidth - step * direction, min, max))
    } else if (e.key === dividerDownKey || (axis === 'x' && e.key === 'ArrowDown')) {
      e.preventDefault()
      onChangeRef.current(clamp(initialWidth + step * direction, min, max))
    }
  }, [initialWidth, min, max, direction, axis])

  return { handleMouseDown, handleKeyDown }
}
