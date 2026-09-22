import { useState, useCallback, useEffect, useId, useRef } from 'react'
import { DownloadCloud, UploadCloud, RefreshCw, Loader2, ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { pull, push, fetch } from '@/api/git'
import { runGitOperation } from '@/lib/gitOperation'
import { useDropdown } from '@/hooks/useDropdown'
import { useGitPanelStore, selectLastOperation } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { GitOperationButton } from './GitOperationButton'
import { GitOperationPopover } from './GitOperationPopover'

type RemoteOp = 'pull' | 'push' | 'fetch'

const OP_LABEL: Record<RemoteOp, string> = {
  pull: 'Pull',
  push: 'Push',
  fetch: 'Fetch',
}

/** Additional flag options offered per remote operation via the chevron dropdown. */
const OP_FLAGS: Record<RemoteOp, { label: string; flags: string[] }[]> = {
  pull: [
    { label: '--ff-only', flags: ['--ff-only'] },
    { label: '--rebase', flags: ['--rebase'] },
    { label: '--rebase --autostash', flags: ['--rebase', '--autostash'] },
  ],
  push: [
    { label: '--force', flags: ['--force'] },
    { label: '--force-with-lease', flags: ['--force-with-lease'] },
    { label: '--no-verify', flags: ['--no-verify'] },
  ],
  fetch: [
    { label: '--tags', flags: ['--tags'] },
    { label: '--prune', flags: ['--prune'] },
  ],
}

/**
 * Remote operations footer (Phase 5): Pull / Push / Fetch, plus a shared
 * git-operation log.
 *
 * Each operation is a split button: the main part runs the default
 * operation, and the chevron opens a dropdown of additional flag options
 * (e.g. pull --rebase, push --force-with-lease). Parallel remote ops are
 * blocked via the shared `remoteOperationInProgress` store flag. An empty
 * `remote` argument lets git use the configured upstream; for push this
 * means the current branch is sent to its upstream, or — when it has never
 * been published — created on the remote and tracked (push -u origin). The
 * backend emits `git:status_changed` after each op, so `useGitStatusEvents`
 * auto-refreshes.
 *
 * The result of every remote op is recorded (via `runGitOperation`) into the
 * store's per-project operation record instead of local state, so the
 * {@link GitOperationButton} can tint green/red from any tab the shared footer
 * is mounted on, and the {@link GitOperationPopover} can replay the captured
 * output. Opening the log acknowledges the record, neutralising the tint.
 */
export function GitPanelFooter() {
  const remoteOperationInProgress = useGitPanelStore(
    (s) => s.remoteOperationInProgress,
  )
  const setRemoteOperationInProgress = useGitPanelStore(
    (s) => s.setRemoteOperationInProgress,
  )
  const acknowledgeOperation = useGitPanelStore((s) => s.acknowledgeOperation)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  // The last operation record by reference (or undefined) — selector-stable.
  const lastOperation = useGitPanelStore((s) => selectLastOperation(s, activeProjectId))

  const [activeOp, setActiveOp] = useState<RemoteOp | null>(null)
  // Open state + outside-click dismissal for the portaled log panel. The panel
  // lives in a separate DOM subtree (document.body), so the hook's `menuRef`
  // — attached to the panel — is what keeps a click INSIDE it from counting as
  // an outside click.
  const {
    isOpen: isLogOpen,
    setIsOpen: setIsLogOpen,
    containerRef: logWrapRef,
    menuRef: logPanelRef,
  } = useDropdown()
  // The trigger, so opening can acknowledge the record and closing (via Escape)
  // can return focus to it — focus moves into the panel on mount.
  const logButtonRef = useRef<HTMLButtonElement>(null)
  // Document-unique id linking the trigger to its panel (`aria-controls`/`id`).
  const logId = useId()

  // Escape closes the log popover and returns focus to its trigger (focus had
  // moved into the panel on open). Outside-click dismissal is the dropdown
  // hook's job; it treats a click inside the portaled panel as "inside".
  useEffect(() => {
    if (!isLogOpen) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setIsLogOpen(false)
        logButtonRef.current?.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [isLogOpen, setIsLogOpen])

  const runOp = useCallback(
    async (op: RemoteOp, flags: string[] = []) => {
      // Records are keyed by the active project, so without one there is
      // nothing to attach the result to. The footer only renders inside a git
      // panel, where a project is always active.
      if (activeProjectId === null) return
      setActiveOp(op)
      setRemoteOperationInProgress(true)
      try {
        // `runGitOperation` never throws: it records the success/failure into
        // the store (which the log button/popover surface) and returns an
        // outcome we intentionally ignore. Empty remote → backend resolves it;
        // push publishes an unpublished branch (push -u <default push remote>)
        // instead of failing.
        await runGitOperation({
          projectId: activeProjectId,
          kind: op,
          label: OP_LABEL[op],
          fn: () =>
            op === 'pull' ? pull('', flags) : op === 'push' ? push('', flags) : fetch('', flags),
          extractOutput: (out) => out || `${OP_LABEL[op]} completed.`,
        })
      } finally {
        setActiveOp(null)
        setRemoteOperationInProgress(false)
      }
    },
    [activeProjectId, setRemoteOperationInProgress],
  )

  const busy = remoteOperationInProgress

  const toggleLog = useCallback(() => {
    if (isLogOpen) {
      setIsLogOpen(false)
      // Return focus to the trigger: focus had moved into the panel on open.
      logButtonRef.current?.focus()
      return
    }
    // Acknowledge as we open: the button tints neutral, so an open log reads
    // as "seen" rather than "unread result". Skip it while an operation is in
    // flight — the result it will record re-arms the tint anyway, and the log
    // stays readable mid-operation.
    if (activeProjectId !== null && !busy) acknowledgeOperation(activeProjectId)
    setIsLogOpen(true)
  }, [isLogOpen, setIsLogOpen, activeProjectId, acknowledgeOperation, busy])

  const buttons: { op: RemoteOp; icon: typeof DownloadCloud }[] = [
    { op: 'fetch', icon: RefreshCw },
    { op: 'pull', icon: DownloadCloud },
    { op: 'push', icon: UploadCloud },
  ]

  return (
    <div className="shrink-0 border-t border-border bg-secondary/30">
      <div className="flex items-center gap-1 px-2 py-1">
        {buttons.map(({ op, icon: Icon }) => (
          <DropdownMenu key={op}>
            <div className="flex items-center">
              <Button
                variant="ghost"
                size="xs"
                disabled={busy}
                onClick={() => void runOp(op)}
                className="gap-1 rounded-r-none text-xs"
                title={OP_LABEL[op]}
              >
                {activeOp === op ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Icon className="size-3.5" />
                )}
                {OP_LABEL[op]}
              </Button>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={busy}
                  className="rounded-l-none border-r border-border/50 px-1"
                  aria-label={`${OP_LABEL[op]} options`}
                >
                  <ChevronDown className="size-3" />
                </Button>
              </DropdownMenuTrigger>
            </div>
            <DropdownMenuContent align="start">
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                {OP_LABEL[op]} options
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              {OP_FLAGS[op].map(({ label, flags }) => (
                <DropdownMenuItem
                  key={label}
                  className="gap-2 font-mono text-xs"
                  onClick={() => void runOp(op, flags)}
                >
                  {label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ))}

        {/* Shared operation log. The panel is portaled to document.body with
            fixed positioning (positioned from the trigger's rect), so it is
            never clipped by the panel's overflow-hidden ancestor. Rendered on
            every tab because the footer itself is shared. */}
        <div ref={logWrapRef} className="ml-auto flex items-center">
          <GitOperationButton
            record={lastOperation}
            busy={busy}
            buttonRef={logButtonRef}
            controlsId={isLogOpen ? logId : undefined}
            open={isLogOpen}
            onToggle={toggleLog}
          />
          {isLogOpen && (
            <GitOperationPopover
              record={lastOperation}
              id={logId}
              triggerRef={logButtonRef}
              panelRef={logPanelRef}
            />
          )}
        </div>
      </div>
    </div>
  )
}
