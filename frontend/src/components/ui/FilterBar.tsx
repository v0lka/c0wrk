import { Regex } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { FilterMode } from '@/lib/pathFilter'

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
}

/**
 * Reusable glob/regex filter bar: a text input plus a toggle button that
 * switches between glob and regex matching modes. Shared by the file-tree
 * panel and the git history panel so the filter UX stays identical.
 */
export function FilterBar({ value, onChange, mode, onToggleMode, placeholder }: FilterBarProps) {
  const label = placeholder ?? 'Filter'
  return (
    <div className="flex shrink-0 gap-1 border-b border-border px-2 py-1">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={`${label}... (${mode})`}
        className="h-7 text-xs"
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
    </div>
  )
}
