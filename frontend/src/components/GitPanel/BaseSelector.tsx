import { useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { BaseSelectorRow } from './BaseSelectorRow'
import type { BranchBase } from '@/types/models'

interface BaseSelectorProps {
  bases: BranchBase[]
  currentBranch: string | null
  selectedBase: string
  onSelect: (ref: string) => void
}

const groupOrder: BranchBase['type'][] = ['local', 'remote', 'tag', 'commit']

const groupLabel: Record<string, string> = {
  local: 'Local branches',
  remote: 'Remote branches',
  tag: 'Tags',
  commit: 'Recent commits',
}

/**
 * Searchable, grouped list of refs usable as a start-point for
 * CreateBranch. Bases are grouped by type (local → remote → tag →
 * commit) and filtered by the search query against ref, label, and
 * detail. The selected base is highlighted and marked with a check.
 */
export function BaseSelector({
  bases,
  currentBranch,
  selectedBase,
  onSelect,
}: BaseSelectorProps) {
  const [query, setQuery] = useState('')

  const grouped = useMemo(() => {
    const q = query.trim().toLowerCase()
    const filtered = q
      ? bases.filter((b) => {
          const hay = `${b.ref} ${b.label} ${b.detail}`.toLowerCase()
          return hay.includes(q)
        })
      : bases

    const groups: Record<string, BranchBase[]> = {
      local: [],
      remote: [],
      tag: [],
      commit: [],
    }
    for (const b of filtered) {
      const bucket = groups[b.type]
      if (bucket) bucket.push(b)
    }
    return groups
  }, [bases, query])

  const hasResults = groupOrder.some((t) => (grouped[t] ?? []).length > 0)

  return (
    <div className="flex flex-col gap-2">
      <div className="relative">
        <Search className="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search base..."
          className="h-8 pl-7 text-xs"
        />
      </div>

      <div className="custom-scrollbar max-h-48 overflow-y-auto rounded-md border border-border">
        {hasResults ? (
          groupOrder.map((type) => {
            const items = grouped[type]
            if (!items || items.length === 0) return null
            return (
              <div key={type}>
                <div className="sticky top-0 bg-background px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  {groupLabel[type]}
                </div>
                {items.map((b) => (
                  <BaseSelectorRow
                    key={`${b.type}-${b.ref}`}
                    base={b}
                    selected={b.ref === selectedBase}
                    isCurrent={b.ref === currentBranch}
                    onSelect={onSelect}
                  />
                ))}
              </div>
            )
          })
        ) : (
          <div className="px-2 py-4 text-center text-xs text-muted-foreground">
            No bases found
          </div>
        )}
      </div>

      {selectedBase && (
        <div className="text-xs text-muted-foreground">
          Base: <span className="font-mono text-foreground">{selectedBase}</span>
        </div>
      )}
    </div>
  )
}
