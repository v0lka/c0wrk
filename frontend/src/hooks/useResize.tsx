import { useRef, useCallback, useState, useEffect, type MouseEvent as ReactMouseEvent } from 'react'

/**
 * useResizeHandle provides drag-to-resize behavior for a panel.
 * `side` determines drag direction: 'left' = drag right to grow, 'right' = drag left to grow.
 */
export function useResizeHandle(
  defaultWidth: number,
  minWidth: number,
  maxWidth: number,
  side: 'left' | 'right'
) {
  const [width, setWidth] = useState(defaultWidth)
  const dragging = useRef(false)
  const startX = useRef(0)
  const startWidth = useRef(0)
  const moveHandlerRef = useRef<((ev: globalThis.MouseEvent) => void) | null>(null)
  const upHandlerRef = useRef<(() => void) | null>(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (moveHandlerRef.current) {
        document.removeEventListener('mousemove', moveHandlerRef.current)
        moveHandlerRef.current = null
      }
      if (upHandlerRef.current) {
        document.removeEventListener('mouseup', upHandlerRef.current)
        upHandlerRef.current = null
      }
      dragging.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
  }, [])

  const onMouseDown = useCallback(
    (e: ReactMouseEvent) => {
      e.preventDefault()
      dragging.current = true
      startX.current = e.clientX
      startWidth.current = width

      const onMouseMove = (ev: MouseEvent) => {
        if (!dragging.current) return
        const delta = ev.clientX - startX.current
        const newWidth =
          side === 'left'
            ? startWidth.current + delta
            : startWidth.current - delta
        setWidth(Math.max(minWidth, Math.min(maxWidth, newWidth)))
      }

      const onMouseUp = () => {
        dragging.current = false
        document.removeEventListener('mousemove', onMouseMove)
        document.removeEventListener('mouseup', onMouseUp)
        document.body.style.cursor = ''
        document.body.style.userSelect = ''
        if (mountedRef.current) {
          moveHandlerRef.current = null
          upHandlerRef.current = null
        }
      }

      // Defensive: remove any existing handlers before adding new ones
      if (moveHandlerRef.current) {
        document.removeEventListener('mousemove', moveHandlerRef.current)
      }
      if (upHandlerRef.current) {
        document.removeEventListener('mouseup', upHandlerRef.current)
      }

      moveHandlerRef.current = onMouseMove
      upHandlerRef.current = onMouseUp

      document.addEventListener('mousemove', onMouseMove)
      document.addEventListener('mouseup', onMouseUp)
      document.body.style.cursor = 'col-resize'
      document.body.style.userSelect = 'none'
    },
    [width, minWidth, maxWidth, side]
  )

  return { width, setWidth, onMouseDown }
}
