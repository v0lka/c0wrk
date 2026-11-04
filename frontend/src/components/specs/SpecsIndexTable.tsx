// INDEX task navigation table (ADR-105): the "task → specs" rows parsed
// from INDEX.md. Each spec target opens in the file viewer through the
// panel's onOpen callback (specs-root-relative; the panel resolves it
// against the absolute root). Renders nothing when the table is empty (a
// tree without an INDEX.md — the findings list carries the INDEX_STALE
// signal).

import { ListChecks } from 'lucide-react'
import type { SpecIndexTask } from '@/types/models'

const LINK_BUTTON_CLASS =
  'rounded px-1 text-info underline decoration-info/40 underline-offset-2 transition-colors hover:bg-muted hover:decoration-info'

/** One row: the task text (INDEX.md's first column, verbatim) and its spec
 *  links — labeled by the target's file name so a long path stays scannable
 *  at sidebar width, with the full relative path on the tooltip. */
function TaskRow({
  task,
  specs,
  onOpen,
}: {
  task: string
  specs: string[]
  onOpen: (absolutePath: string) => void
}) {
  return (
    <div className="flex items-start gap-1.5 py-0.5 text-xs leading-relaxed">
      <span className="min-w-0 flex-1 break-words text-foreground">{task}</span>
      <span className="flex max-w-[55%] shrink-0 flex-wrap justify-end gap-x-1.5 gap-y-0.5">
        {specs.map((spec) => (
          <button
            key={spec}
            type="button"
            data-testid="specs-index-link"
            data-spec={spec}
            title={spec}
            onClick={() => onOpen(spec)}
            className={LINK_BUTTON_CLASS}
          >
            {spec.split('/').pop() ?? spec}
          </button>
        ))}
      </span>
    </div>
  )
}

/** The INDEX section: header + one row per parsed task. `specs` entries are
 *  specs-root-relative references; the panel resolves them against the
 *  absolute root before opening. */
export function SpecsIndexTable({
  indexTasks,
  onOpen,
}: {
  indexTasks: SpecIndexTask[]
  onOpen: (relPath: string) => void
}) {
  if (indexTasks.length === 0) return null
  return (
    <section data-testid="specs-index" className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1 px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        <ListChecks className="size-3" />
        INDEX — tasks
      </div>
      {indexTasks.map((row) => (
        <TaskRow
          key={row.task}
          task={row.task}
          specs={row.specs ?? []}
          onOpen={onOpen}
        />
      ))}
    </section>
  )
}
