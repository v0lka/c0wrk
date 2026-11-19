import { Columns2, Rows3 } from 'lucide-react'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { useReviewStore } from '@/stores/reviewStore'

interface DiffViewModeToggleProps {
  className?: string
}

/**
 * Unified / split diff view-mode toggle — the SHARED app-wide control.
 *
 * Extracted from ReviewHeader so every diff-rendering surface (the review
 * page headers and the file viewer's Git-graph page) drives the same
 * preference: a global setting (no session affinity) stored as
 * `reviewStore.diffViewMode` and persisted with the store. The component
 * binds to the store itself — callers render `<DiffViewModeToggle />` and
 * never thread the mode through props. Shared segmented control (ARIA tabs,
 * icon-only items with tooltips, stable `diff-view-*` test ids).
 */
export function DiffViewModeToggle({ className }: DiffViewModeToggleProps) {
  const diffViewMode = useReviewStore((s) => s.diffViewMode)
  const setDiffViewMode = useReviewStore((s) => s.setDiffViewMode)
  return (
    <SegmentedControl
      className={className}
      items={[
        {
          value: 'unified',
          icon: <Rows3 className="size-3.5" />,
          title: 'Unified view',
          testId: 'diff-view-unified',
        },
        {
          value: 'split',
          icon: <Columns2 className="size-3.5" />,
          title: 'Split view',
          testId: 'diff-view-split',
        },
      ]}
      value={diffViewMode}
      onValueChange={setDiffViewMode}
      ariaLabel="Diff view mode"
    />
  )
}
