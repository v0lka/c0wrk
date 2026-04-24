import { cn } from '@/lib/utils'

interface ResizeHandleProps {
  onMouseDown: (e: React.MouseEvent) => void
  onKeyDown: (e: React.KeyboardEvent) => void
  className?: string
  orientation?: 'vertical' | 'horizontal'
}

export function ResizeHandle({
  onMouseDown,
  onKeyDown,
  className,
  orientation = 'vertical',
}: ResizeHandleProps) {
  return (
    <div
      className={cn(
        'flex-shrink-0 transition-colors',
        orientation === 'vertical'
          ? 'w-1 cursor-col-resize'
          : 'h-1 cursor-row-resize',
        'bg-transparent hover:bg-ring active:bg-primary',
        className,
      )}
      onMouseDown={onMouseDown}
      onKeyDown={onKeyDown}
      role="separator"
      aria-orientation={orientation}
      aria-label="Resize panel"
      tabIndex={0}
    />
  )
}
