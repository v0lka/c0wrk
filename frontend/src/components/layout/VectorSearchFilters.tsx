import { useRef } from 'react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Search, X } from 'lucide-react'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import type { SearchMode } from '@/types/models'

const MODES: SearchMode[] = ['hybrid', 'vector', 'lexical']

interface VectorSearchFiltersProps {
  isSearchMode: boolean
  onSearch: () => void
  onClear: () => void
  onKeyDown: (e: React.KeyboardEvent) => void
}

/**
 * VectorSearchFilters renders the query / topK / file-pattern inputs, the
 * search/clear buttons, the mode selector, and the must-match chips.
 * State lives in useVectorIndexStore; handlers come from useVectorSearch. (W-30)
 */
export function VectorSearchFilters({ isSearchMode, onSearch, onClear, onKeyDown }: VectorSearchFiltersProps) {
  const status = useVectorIndexStore((s) => s.status)
  const isLoading = useVectorIndexStore((s) => s.isLoading)
  const query = useVectorIndexStore((s) => s.query)
  const topK = useVectorIndexStore((s) => s.topK)
  const filePattern = useVectorIndexStore((s) => s.filePattern)
  const mustMatch = useVectorIndexStore((s) => s.mustMatch)
  const mode = useVectorIndexStore((s) => s.mode)
  const setQuery = useVectorIndexStore((s) => s.setQuery)
  const setTopK = useVectorIndexStore((s) => s.setTopK)
  const setFilePattern = useVectorIndexStore((s) => s.setFilePattern)
  const removeMustMatch = useVectorIndexStore((s) => s.removeMustMatch)
  const setMode = useVectorIndexStore((s) => s.setMode)

  const queryInputRef = useRef<HTMLInputElement>(null)

  // While the index is not ready, searches would block on index readiness —
  // the query/file-pattern inputs are disabled at the widget level (the
  // search button below is gated the same way).
  const searchDisabled = status.state !== 'ready'

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex gap-1">
        <Input
          ref={queryInputRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={searchDisabled}
          placeholder="Keywords... (+tok forces match)"
          className="h-7 text-xs"
        />
        <Input
          type="number"
          value={topK}
          onChange={(e) => setTopK(Math.max(1, parseInt(e.target.value) || 50))}
          min={1}
          max={500}
          className="h-7 w-20 text-xs text-center"
        />
      </div>
      <div className="flex gap-1">
        <Input
          value={filePattern}
          onChange={(e) => setFilePattern(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={searchDisabled}
          placeholder="File pattern (e.g. *.go, src/**)"
          className="h-7 text-xs"
        />
        <Button
          variant="default"
          size="sm"
          onClick={onSearch}
          disabled={isLoading || searchDisabled}
          className="h-7 px-2"
        >
          <Search className="size-3.5" />
        </Button>
        {isSearchMode && (
          <Button
            variant="ghost"
            size="sm"
            onClick={onClear}
            className="h-7 px-2"
          >
            <X className="size-3.5" />
          </Button>
        )}
      </div>

      {/* Mode selector */}
      <div className="flex gap-1">
        {MODES.map((m) => (
          <Button
            key={m}
            variant={mode === m ? 'default' : 'ghost'}
            size="sm"
            onClick={() => setMode(m)}
            className="h-6 flex-1 px-2 text-[11px] capitalize"
          >
            {m}
          </Button>
        ))}
      </div>

      {/* MustMatch chips */}
      {mustMatch.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {mustMatch.map((tok) => (
            <button
              key={tok}
              type="button"
              onClick={() => removeMustMatch(tok)}
              className="flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-[10px] hover:bg-destructive hover:text-destructive-foreground"
              title="Click to remove"
            >
              <span>+{tok}</span>
              <X className="size-2.5" />
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
