import { useCallback, useState } from 'react'
import { Crosshair } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { focusSessionWorkspace } from '@/lib/gitFocus'

/**
 * The Git-panel focus button (crosshair) in the Files/Changes/History
 * header row. Clicking it focuses the panel on the CURRENT SESSION's
 * worktree — the default target — snapping the panel back after an
 * explicit switch to another tree. It is highlighted (aria-pressed) while
 * the focus has diverged from the session's worktree; the title always
 * names the current focus target so the divergence is discoverable.
 *
 * The button is an EXPERIMENTAL surface behind the master experimental switch
 * (`experimental.enabled`): it renders nothing while that switch is off. This
 * is a VISIBILITY gate only — the focus functionality itself is untouched.
 */
export function GitFocusButton() {
  const focus = useGitPanelStore((s) => s.focus)
  const focusSessionPath = useGitPanelStore((s) => s.focusSessionPath)
  const [busy, setBusy] = useState(false)
  // The master experimental switch — the crosshair is an experimental
  // surface, so it stays invisible in default installs (read reactively, so
  // flipping the switch in Settings reveals it without a reload). Read from
  // the store directly (no config fetch side effect here).
  const experimentalEnabled = useExperimentalStore((s) => s.enabled)

  const diverged =
    focus !== null && focusSessionPath !== null && focus.path !== focusSessionPath

  const onClick = useCallback(() => {
    setBusy(true)
    focusSessionWorkspace().finally(() => setBusy(false))
  }, [])

  // Experimental gate (visibility only): hide the crosshair while the master
  // switch is off. Placed after the hooks above.
  if (!experimentalEnabled) return null

  const title = focus
    ? `Git focus: ${focus.name}${focus.branch ? ` (${focus.branch})` : ''}${
        diverged ? ' — click to focus the current session' : ''
      }`
    : 'Focus the Git panel on the current session'

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      title={title}
      aria-label={title}
      aria-pressed={diverged}
      data-testid="git-focus-button"
      data-diverged={diverged || undefined}
      className={`mr-1 flex size-7 shrink-0 items-center justify-center rounded-md transition-colors focus:outline-none focus:ring-1 focus:ring-ring ${
        diverged
          ? 'bg-primary/15 text-primary'
          : 'text-muted-foreground hover:bg-muted hover:text-foreground'
      }`}
    >
      <Crosshair className="size-4" />
    </button>
  )
}
