import { CaseSensitive, Regex } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { HistorySearchMode } from '@/hooks/useGitHistorySearch'
import { cn } from '@/lib/utils'

interface HistorySearchBarProps {
  value: string
  onChange: (value: string) => void
  mode: HistorySearchMode
  onToggleMode: () => void
  /** Extra classes merged into the root row — the toolbar gives this bar
   *  an equal flex share beside the file filter. */
  className?: string
}

/**
 * The history page's search bar: a pattern input plus the text/regex
 * toggle, mirroring FilterBar's visual language (same input/button heights,
 * the toggle lights up in regex mode). Text mode matches metadata as a
 * substring and pickaxe-searches the literal string (-S); regex mode
 * matches metadata as a regex and pickaxe-searches -G.
 */
export function HistorySearchBar({ value, onChange, mode, onToggleMode, className }: HistorySearchBarProps) {
  return (
    <div className={cn('flex shrink-0 gap-1 px-2 py-1', className)}>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={`Search history... (${mode})`}
        className="h-7 min-w-0 flex-1 text-xs"
      />
      <Button
        variant={mode === 'regex' ? 'default' : 'ghost'}
        size="sm"
        onClick={onToggleMode}
        className="h-7 px-2"
        title={mode === 'text' ? 'Switch to regex search' : 'Switch to text search'}
      >
        {mode === 'text' ? <CaseSensitive className="size-3.5" /> : <Regex className="size-3.5" />}
      </Button>
    </div>
  )
}
