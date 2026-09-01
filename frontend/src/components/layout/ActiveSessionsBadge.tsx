// The overlapping dot cluster rendered on the Radar indicator button
// (ActiveSessionsIndicator). Kept next to it as its own file so both stay
// under the 200-line component target; purely presentational — no store
// access, exported for unit tests.

import { cn } from '@/lib/utils'
import type { BadgeFlags } from '@/lib/activeSessions'

/** One dot per flag, in fixed render order: red → yellow → green → gray. */
const DOT_SPECS = [
  { flag: 'error', className: 'bg-destructive' },
  { flag: 'attention', className: 'bg-warning' },
  { flag: 'active', className: 'bg-success' },
  { flag: 'paused', className: 'bg-muted-foreground' },
] as const satisfies ReadonlyArray<{ flag: keyof Omit<BadgeFlags, 'anyLive'>; className: string }>

/**
 * The overlapping dot cluster, anchored to the bottom-right corner of the
 * parent (the indicator button must be `relative`). Dots render in DOT_SPECS
 * order; every dot after the first shifts left with -ml-1 so the cluster
 * partially overlaps, later dots painting on top of earlier ones. The
 * ring-background ring separates the dots from the Radar icon underneath.
 */
export function ActiveSessionsBadge({ flags }: { flags: BadgeFlags }) {
  const dots = DOT_SPECS.filter((spec) => flags[spec.flag])
  if (dots.length === 0) return null
  return (
    <span className="pointer-events-none absolute right-0 bottom-0 flex items-center" aria-hidden="true">
      {dots.map((spec, index) => (
        <span
          key={spec.flag}
          className={cn('size-2 rounded-full ring-1 ring-background', spec.className, index > 0 && '-ml-1')}
        />
      ))}
    </span>
  )
}
