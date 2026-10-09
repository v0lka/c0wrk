import { useEffect, useId } from 'react'
import { GitBranch, FolderTree } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useExperimentalStore } from '@/stores/experimentalStore'

/**
 * The chat toolbar's session-workspace selector (ADR-080 draft UX).
 *
 * - While a session draft is pending (New Session armed, no session row
 *   yet), it is the interactive draft choice: `local` (the project working
 *   tree) or `branch…` — the latter opens the BranchPicker in its
 *   intent-separated draft mode, where selecting an existing branch or
 *   creating a new one RECORDS the choice; provisioning happens at draft
 *   commit (first send / attachment / terminal open) via
 *   CreateManagedSession, never as an in-place checkout.
 * - Once a session exists, the selector shows that session's pinned
 *   workspace as a permanently disabled CHIP — the branch/workspace is
 *   immutable for an existing session (chosen only at creation), so choosing
 *   another one is impossible forever. The disabled state must state itself
 *   (dimmed, cursor-not-allowed) instead of an enabled-looking control that
 *   silently swallows clicks, but it renders as `aria-disabled="true"`
 *   rather than the native `disabled` attribute (see the accessibility note
 *   at the render site: a natively disabled control leaves the tab order —
 *   its reason text becomes unreachable — and suppresses pointer events,
 *   which breaks the tooltip in the WebKit webview). The pin is
 *   unconditional: it deliberately ignores the transient `disabled` prop
 *   (the mid-task selector lock) — that lock lifts when the task settles,
 *   while the pin never does, so the chip stays disabled across chat
 *   continuations for the session's whole lifetime.
 *
 * The whole picker/display is an EXPERIMENTAL surface behind the master
 * experimental switch (`experimental.enabled`): it renders nothing while that
 * switch is off. This is a VISIBILITY gate only — the underlying
 * draft/pinned-workspace functionality is untouched, and the hooks run before
 * the early return so draft retirement still works while hidden.
 *
 * Hidden entirely in CHAT (No Project) mode and while no project is active.
 */
export function SessionWorkspaceSelector({ disabled = false }: { disabled?: boolean }) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const sessions = useSessionStore((s) => s.sessions)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = useProjectStore(selectIsNoProject)
  const draft = useSessionDraftStore((s) => s.draft)
  const committing = useSessionDraftStore((s) => s.committing)
  // Paired per the store contract: a stale repo answer from a previously
  // active project must never show the `branch…` entry here.
  const isGitRepo = useGitPanelStore(
    (s) => s.isGitRepo && s.gitRepoProjectId === activeProjectId,
  )
  // The master experimental switch — this picker/display is an experimental
  // surface, so it stays invisible in default installs (read reactively, so
  // flipping the switch in Settings reveals it without a reload). Read from
  // the store directly (no config fetch side effect here).
  const experimentalEnabled = useExperimentalStore((s) => s.enabled)
  // Accessible-description target for the pinned-workspace chip's reason
  // (see the aria-describedby note below). Declared before the early returns.
  const reasonId = useId()

  // Draft retirement: selecting an existing session (or landing in another
  // project) abandons the pending draft — the workspace is a
  // creation-time-only choice, so an armed draft must not outlive the
  // gesture that started it. Runs even when the selector itself renders
  // nothing (CHAT mode / no project), because the hooks above are
  // unconditional.
  useEffect(() => {
    const { draft: current, clearDraft } = useSessionDraftStore.getState()
    if (current === null) return
    if (activeSessionId !== null || current.projectId !== activeProjectId) {
      clearDraft()
    }
  }, [activeSessionId, activeProjectId])

  // Experimental gate (visibility only): hide the picker/display while the
  // master switch is off. Placed after the hooks above so draft retirement
  // keeps running even when the control is hidden.
  if (!experimentalEnabled) return null

  if (activeProjectId === null || isNoProject) return null

  // An existing session pins its workspace: the binding is immutable after
  // creation (ADR-080), so choosing another branch/workspace is impossible —
  // forever, chat continuations included. The pin renders as a PERMANENTLY
  // DISABLED button rather than an enabled-looking non-interactive element:
  // the disabled state must be visible up front, not discovered by clicks
  // that go nowhere. It deliberately ignores the transient `disabled` prop
  // (the mid-task selector lock) — that lock lifts when the task settles;
  // the pin never does. While the session list has not loaded the entry yet
  // (transient switch window), render nothing rather than guess the binding.
  //
  // Accessibility: the chip uses `aria-disabled="true"` instead of the native
  // `disabled` attribute — a natively disabled control is removed from the
  // tab order (its reason text unreachable by keyboard/screen-reader) and
  // suppresses pointer events, which makes both the `title` tooltip and the
  // not-allowed cursor unreliable in the WebKit webview. The pin reason
  // travels in a visually-hidden element referenced by `aria-describedby`
  // (announced with the chip) and the `title` stays as the mouse tooltip.
  if (activeSessionId !== null) {
    const activeSession = sessions?.find((s) => s.id === activeSessionId) ?? null
    if (!activeSession) return null
    const binding = activeSession.workspace_binding
    const isManaged = binding?.kind === 'managed_worktree'
    const label = isManaged ? binding?.branch || 'branch' : 'local'
    const pinReason = isManaged
      ? `Managed worktree on ${label} — fixed when the session was created`
      : 'Project working tree — fixed when the session was created'
    return (
      <button
        type="button"
        aria-disabled="true"
        data-testid="session-workspace-readonly"
        aria-label={`Session workspace: ${label}`}
        aria-describedby={reasonId}
        title={pinReason}
        className={cn(
          'flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background',
          'text-muted-foreground max-w-[200px] truncate',
          'opacity-50 cursor-not-allowed',
        )}
      >
        {isManaged ? (
          <GitBranch className="size-3 shrink-0" />
        ) : (
          <FolderTree className="size-3 shrink-0" />
        )}
        <span className="truncate">{label}</span>
        <span id={reasonId} className="sr-only">
          {pinReason}
        </span>
      </button>
    )
  }

  // Draft mode: no session exists yet — the selector picks the workspace the
  // NEXT created session will run in. A draft from another project is
  // treated as absent (and retired by the effect above).
  const workspace =
    draft !== null && draft.projectId === activeProjectId ? draft.workspace : { kind: 'local' as const }
  const draftLabel = workspace.kind === 'branch' ? workspace.branch : 'local'
  const locked = disabled || committing

  const chooseLocal = () => {
    // A bare no-session state already means "next session is local" — only
    // an armed draft needs an explicit write.
    if (draft === null) return
    useSessionDraftStore.getState().setDraftWorkspace({ kind: 'local' })
  }

  const openDraftBranchPicker = () => {
    if (draft === null) {
      useSessionDraftStore.getState().startDraft(activeProjectId)
    }
    useGitPanelStore.getState().openBranchPicker('draft')
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={locked}>
        <button
          type="button"
          disabled={locked}
          aria-label="New session workspace"
          data-testid="session-workspace-selector"
          className={cn(
            'flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background',
            'hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors',
            'max-w-[200px] truncate disabled:opacity-50 disabled:cursor-not-allowed',
          )}
          title={
            workspace.kind === 'branch'
              ? `New session on managed worktree branch ${workspace.branch}`
              : 'New session in the project working tree'
          }
        >
          {workspace.kind === 'branch' ? (
            <GitBranch className="size-3 shrink-0" />
          ) : (
            <FolderTree className="size-3 shrink-0" />
          )}
          <span className="truncate">{draftLabel}</span>
          <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M6 9l6 6 6-6" />
          </svg>
        </button>
      </DropdownMenuTrigger>

      <DropdownMenuContent aria-label="New session workspace" align="start" className="min-w-56">
        <DropdownMenuItem
          data-testid="workspace-option-local"
          onSelect={chooseLocal}
          aria-current={workspace.kind === 'local' ? 'true' : undefined}
        >
          <FolderTree className="size-3.5" />
          <span className="flex-1">local</span>
          <span className="text-xs text-muted-foreground">project working tree</span>
        </DropdownMenuItem>
        {isGitRepo && (
          <DropdownMenuItem data-testid="workspace-option-branch" onSelect={openDraftBranchPicker}>
            <GitBranch className="size-3.5" />
            <span className="flex-1">branch…</span>
            <span className="text-xs text-muted-foreground">managed worktree</span>
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
