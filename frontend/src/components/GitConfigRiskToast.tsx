// Git-config risk toast — warns when a newly opened project or added work
// directory carries a dangerous .git/config (command-bearing filters, merge
// drivers, textconv, fsmonitor, hooksPath, ...).
//
// Self-contained: subscribes to the global `project:git_config_risk` event via
// the @/api/gitConfigRisk wrapper and renders nothing until a warning arrives.
// A clean repository emits nothing, so the toast stays hidden. A newer event
// replaces the currently shown one; dismissal is manual — this is a security
// notice the user is expected to read, not transient progress feedback.

import { useCallback, useEffect, useState } from 'react'
import { ShieldAlert, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { onGitConfigRisk } from '@/api/gitConfigRisk'
import type { GitConfigRiskData } from '@/types/events'

function sourceLabel(source: GitConfigRiskData['source']): string {
  return source === 'workdir' ? 'Added working directory' : 'Project'
}

export function GitConfigRiskToast() {
  const [risk, setRisk] = useState<GitConfigRiskData | null>(null)

  useEffect(
    () =>
      onGitConfigRisk((data) => {
        setRisk(data)
      }),
    [],
  )

  const dismiss = useCallback(() => {
    setRisk(null)
  }, [])

  if (!risk) return null

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
          </div>
          <button
            onClick={dismiss}
            className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="Dismiss"
          >
            <X className="size-4" />
          </button>
        </div>
      </div>
    </div>
  )
}
