import { type MouseEvent as ReactMouseEvent } from 'react'

export interface ResizeHandleProps {
  onMouseDown: (e: ReactMouseEvent) => void
  onResize: (delta: number) => void
}

export function ResizeHandle({ onMouseDown, onResize }: ResizeHandleProps) {
  return (
    <div
      className="w-1 flex-shrink-0 bg-border hover:bg-ring active:bg-ring transition-colors cursor-col-resize focus:outline-none focus:bg-ring"
      onMouseDown={onMouseDown}
      role="separator"
      aria-label="Resize panel"
      aria-orientation="vertical"
      tabIndex={0}
      onKeyDown={(e) => {
        const step = e.shiftKey ? 50 : 10
        if (e.key === 'ArrowLeft') {
          e.preventDefault()
          onResize(-step)
        } else if (e.key === 'ArrowRight') {
          e.preventDefault()
          onResize(step)
        }
      }}
    />
  )
}
