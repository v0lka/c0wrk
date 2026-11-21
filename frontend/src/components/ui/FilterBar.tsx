import { Regex } from 'lucide-react'
import type { ReactNode } from 'react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { FilterMode } from '@/lib/pathFilter'
import { cn } from '@/lib/utils'

interface FilterBarProps {
  value: string
  onChange: (value: string) => void
  mode: FilterMode
  onToggleMode: () => void
  /**
   * Base placeholder text; the active mode is appended, e.g.
   * "Filter files" → "Filter files... (glob)".
   */
  placeholder?: string
  /** Optional content rendered BEFORE the input (e.g. the file tree's
   *  open-workspace / open-temp buttons); the input shrinks to fit. */
  leftSlot?: ReactNode
  /** Optional content rendered after the mode toggle (e.g. a refresh
   *  action owned by the embedding panel). */
  rightSlot?: ReactNode
  /**
   * Extra classes merged into the root row — e.g. `flex-1 border-b-0` when
   * the embedding row places sibling controls beside the bar (the Git
   * history page's diff-mode toggle). Without it the bar keeps its
   * standalone full-width form.
   */
  className?: string
}

/**
 * Reusable glob/regex filter bar: a text input plus a toggle button that
 * switches between glob and regex matching modes. Shared by the file-tree
 * panel and the viewer's Git history page so the filter UX stays identical.
 */
export function FilterBar({ value, onChange, mode, onToggleMode, placeholder, leftSlot, rightSlot, className }: FilterBarProps) {
  const label = placeholder ?? 'Filter'
  return (
    <div className={cn('flex shrink-0 gap-1 border-b border-border px-2 py-1', className)}>
      {leftSlot}
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={`${label}... (${mode})`}
        className="h-7 min-w-0 flex-1 text-xs"
      />
      <Button
        variant={mode === 'regex' ? 'default' : 'ghost'}
        size="sm"
        onClick={onToggleMode}
        className="h-7 px-2"
        title={mode === 'glob' ? 'Switch to regex' : 'Switch to glob'}
      >
        <Regex className="size-3.5" />
      </Button>
      {rightSlot}
    </div>
  )
}
