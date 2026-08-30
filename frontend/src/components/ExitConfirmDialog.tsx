// Exit confirmation modal — the frontend half of the desktop close guard.
//
// A pure view over `exitGuardStore` (written by the useExitGuard
// subscription at the app root): when the backend intercepts a quit because
// sessions have live work (app:exit_requested), this modal asks the user to
// confirm. "Cancel" dismisses it (the guard stays armed); "Quit anyway"
// calls the ConfirmExit RPC which bypasses the guard and quits gracefully,
// so running tasks are cancelled by the normal Shutdown drain instead of a
// force-kill. When the intercepted quit belongs to a pending self-update
// (ApplyUpdate), the dialog presents restart context instead.
//
// The dialog renders its generic variant (no session list) when the store
// holds an empty list with `open === true` — the payload failed validation,
// but the quit still needs an answer.

import { useRef, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { confirmExit } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useExitGuardStore } from '@/stores/exitGuardStore'

/** Human label for one active session's live work. */
function sessionActivityLabel(compacting: boolean): string {
  return compacting ? 'compacting context' : 'running task'
}

/** Pluralized live-work summary for a known session count. */
function sessionsSummary(count: number): string {
  if (count === 1) return '1 session is still working. Quitting now will interrupt it.'
  return `${count} sessions are still working. Quitting now will interrupt them.`
}

export function ExitConfirmDialog() {
  const open = useExitGuardStore((s) => s.open)
  const sessions = useExitGuardStore((s) => s.sessions)
  const updatePending = useExitGuardStore((s) => s.updatePending)
  const clear = useExitGuardStore((s) => s.clear)
  // In-flight + failure state of the ConfirmExit RPC: a persistently failing
  // binding must never leave the modal a silent dead end (every click doing
  // nothing with no feedback) — the error renders inline and the user can
  // retry or cancel. The ref is the synchronous double-click guard (state
  // does not commit between same-tick clicks); the state drives the UI.
  const confirmingRef = useRef(false)
  const [isConfirming, setIsConfirming] = useState(false)
  const [confirmError, setConfirmError] = useState<string | null>(null)

  const count = sessions.length
  const hasList = count > 0

  // On success the app quits (the window goes away with it); on failure the
  // modal stays open with the error rendered inline so the user can retry or
  // cancel. The in-flight guard also absorbs double-clicks (one RPC).
  const confirm = () => {
    if (confirmingRef.current) return
    confirmingRef.current = true
    setIsConfirming(true)
    setConfirmError(null)
    confirmExit()
      .catch((err) => {
        logger.error('Failed to confirm quit despite active sessions:', err)
        setConfirmError(err instanceof Error ? err.message : 'Failed to quit — the backend did not accept the confirmation.')
      })
      .finally(() => {
        confirmingRef.current = false
        setIsConfirming(false)
      })
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) clear() }}>
      <DialogContent className="sm:max-w-md" showCloseButton={false}>
        <DialogHeader>
          <div className="flex items-center gap-3">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-warning/15">
              <AlertTriangle className="size-5 text-warning" />
            </div>
            <div className="flex flex-col gap-1">
              <DialogTitle>{updatePending ? 'Restart & Update?' : 'Quit c0wrk?'}</DialogTitle>
              <DialogDescription>
                {hasList
                  ? sessionsSummary(count)
                  : 'Some sessions are still working. Quitting now may interrupt them.'}
                {updatePending
                  ? ' The staged update will be installed and c0wrk will restart.'
                  : ' Unfinished work cannot be resumed after the app closes.'}
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        {hasList && (
          <ul className="max-h-40 overflow-y-auto custom-scrollbar rounded-md border border-border bg-muted/30 px-3 py-2">
            {sessions.map((s) => (
              <li
                key={s.id}
                className="flex items-center justify-between gap-3 py-1 text-sm"
              >
                <span className="min-w-0 truncate" title={s.name || s.id}>
                  {s.name || s.id}
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {sessionActivityLabel(s.compacting)}
                </span>
              </li>
            ))}
          </ul>
        )}

        {confirmError && (
          <div className="text-xs text-destructive" role="alert">
            {confirmError}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={clear} autoFocus disabled={isConfirming}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={confirm} disabled={isConfirming}>
            {updatePending ? 'Restart & Update' : 'Quit anyway'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
