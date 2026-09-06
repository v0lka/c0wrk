// Git-config risk toast — warns when a newly opened project or added work
// directory carries a dangerous .git/config (command-bearing filters, merge
// drivers, textconv, fsmonitor, hooksPath, ...).
//
// Self-contained: subscribes to the global `project:git_config_risk` event via
// the @/api/gitConfigRisk wrapper and renders nothing until a warning arrives.
// A clean repository emits nothing, so the toast stays hidden. A newer event
// replaces the currently shown one. Two decisions are offered, plus a close:
//   • "Trust this repo" — persists the repo root into security.trusted_git_repos
//     (backend RPC, bound to a fingerprint+snapshot of the scanned config); the
//     warning is suppressed for this repository on future opens until the config
//     drifts. The git subprocess hardening turns off for it (its own hooks,
//     filters and signing apply).
//   • "Harden" — persists the repo root into security.harden_git_repos (backend
//     RPC); the repository is always neutralized and can never become raw-git
//     eligible. Hardening a trusted repo drops the trust.
//   • The close (×) — dismisses the warning without deciding (the repo stays
//     "pending" and re-warns on the next open).
//
// When the warning fires because a previously-trusted repository's configuration
// changed (payload `reason` set), the toast additionally shows the
// re-confirmation text and the config diff so the user can judge whether the
// change was expected before re-trusting.
//
// The warning is dropped when the user switches to a different project: a
// decision must never be recorded for the wrong repository. The backend re-emits
// the warning whenever the repository is opened again.

import { useCallback, useEffect, useState } from 'react'
import { ShieldAlert, ShieldBan, ShieldCheck, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { onGitConfigRisk, trustGitRepo, hardenGitRepo } from '@/api/gitConfigRisk'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useProjectStore } from '@/stores/projectStore'
import { isProjectInfo } from '@/types/guards'
import type { GitConfigRiskData } from '@/types/events'

/** Everything the toast shows about ONE warning, bundled so the async actions
 *  (trust/harden RPCs) act strictly on the warning they were started for: a
 *  newer event replaces the whole bundle, so a late resolve or failure of an
 *  older action can neither close nor annotate the new warning. */
interface RiskToastState {
  risk: GitConfigRiskData
  /** Project that was active when the warning arrived — the warning belongs
   *  to that project's workspace (its root or one of its workdirs). */
  projectId: string | null
  actionError: string | null
  trustPending: boolean
  hardenPending: boolean
}

function sourceLabel(source: GitConfigRiskData['source']): string {
  return source === 'workdir' ? 'Added working directory' : 'Project'
}

export function GitConfigRiskToast() {
  const [state, setState] = useState<RiskToastState | null>(null)

  useEffect(
    () =>
      onGitConfigRisk((data) => {
        setState({
          risk: data,
          projectId: useProjectStore.getState().activeProjectId,
          actionError: null,
          trustPending: false,
          hardenPending: false,
        })
      }),
    [],
  )

  // Drop the warning on a switch to a different project (same-id
  // reconciliation re-emits keep it). Acting on the project:switched event
  // itself — not on an activeProjectId store effect — is race-free: the
  // backend emits project:switched BEFORE project:git_config_risk for the
  // project being opened, so this can never clear a warning that belongs to
  // the project the user just switched to.
  useEffect(
    () =>
      subscribe('project:switched', (data) => {
        if (!isProjectInfo(data)) return
        setState((prev) => (prev && prev.projectId !== data.id ? null : prev))
      }),
    [],
  )

  const dismiss = useCallback(() => {
    setState(null)
  }, [])

  const handleTrust = useCallback(async () => {
    if (!state || state.trustPending) return
    // Pin the warning this action acts on — a newer event may replace the
    // shown warning while the RPC is in flight.
    const startedPath = state.risk.path
    setState({ ...state, trustPending: true, actionError: null })
    try {
      await trustGitRepo(startedPath)
      setState((prev) => (prev?.risk.path === startedPath ? null : prev))
    } catch (err) {
      logger.error('Trust this repo failed:', err)
      const message = err instanceof Error ? err.message : String(err)
      setState((prev) =>
        prev?.risk.path === startedPath ? { ...prev, actionError: message, trustPending: false } : prev,
      )
    }
  }, [state])

  const handleHarden = useCallback(async () => {
    if (!state || state.hardenPending) return
    const startedPath = state.risk.path
    setState({ ...state, hardenPending: true, actionError: null })
    try {
      await hardenGitRepo(startedPath)
      setState((prev) => (prev?.risk.path === startedPath ? null : prev))
    } catch (err) {
      logger.error('Harden this repo failed:', err)
      const message = err instanceof Error ? err.message : String(err)
      setState((prev) =>
        prev?.risk.path === startedPath ? { ...prev, actionError: message, hardenPending: false } : prev,
      )
    }
  }, [state])

  if (!state) return null

  const { risk, actionError, trustPending, hardenPending } = state
  const busy = trustPending || hardenPending

  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex flex-col items-end">
      <div
        role="alert"
        className={cn(
          // Same surface as UpdateToast (bottom-right, popover background),
          // with a warning-tinted border to signal "read me", not "error".
          'pointer-events-auto w-96 rounded-lg border border-warning/50 bg-popover p-4 shadow-lg',
          'animate-in fade-in slide-in-from-bottom-2 duration-200',
        )}
        data-testid="git-config-risk-toast"
      >
        <div className="flex items-start gap-3">
          <ShieldAlert className="mt-0.5 size-5 shrink-0 text-warning" />
          <div className="min-w-0 flex-1">
            <p className="font-semibold">Untrusted git configuration detected</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {sourceLabel(risk.source)}: <span className="font-mono">{risk.path}</span>
            </p>
            <p className="mt-2 text-sm">{risk.notice}</p>
            <ul className="mt-2 max-h-64 space-y-1.5 overflow-y-auto pr-1 custom-scrollbar">
              {risk.findings.map((f, i) => (
                // Composite key: the same key can legitimately repeat — a
                // config may define one key in several sections, and
                // multiple include directives share the synthetic
                // "(include directive)" marker.
                <li key={`${i}-${f.key}`} className="text-xs">
                  <span className="rounded bg-warning/10 px-1.5 py-0.5 font-mono font-medium text-warning">
                    {f.key}
                  </span>
                  <span className="ml-1.5 text-muted-foreground">{f.description}</span>
                </li>
              ))}
            </ul>
            {risk.reason && (
              <p
                className="mt-2 text-sm text-warning"
                data-testid="git-config-risk-reason"
              >
                {risk.reason}
              </p>
            )}
            {risk.diff && (
              <pre
                className="mt-2 max-h-56 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/50 p-2 font-mono text-xs"
                data-testid="git-config-risk-diff"
              >
                {risk.diff}
              </pre>
            )}
            {actionError && (
              <p className="mt-2 text-xs text-destructive" data-testid="git-config-risk-error">
                {actionError}
              </p>
            )}
            <div className="mt-3 flex items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void handleTrust()}
                disabled={busy}
                data-testid="git-config-risk-trust"
              >
                <ShieldCheck className="size-3.5" />
                {trustPending ? 'Trusting…' : 'Trust this repo'}
              </Button>
              <Button
                size="sm"
                variant="default"
                onClick={() => void handleHarden()}
                disabled={busy}
                data-testid="git-config-risk-harden"
              >
                <ShieldBan className="size-3.5" />
                {hardenPending ? 'Hardening…' : 'Harden'}
              </Button>
            </div>
          </div>
          <button
            onClick={dismiss}
            disabled={busy}
            className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50 disabled:pointer-events-none"
            aria-label="Dismiss"
            data-testid="git-config-risk-close"
          >
            <X className="size-4" />
          </button>
        </div>
      </div>
    </div>
  )
}
