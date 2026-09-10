// Small error banner shared by the research workspace's enabled and
// disabled renderings (status-loading failures, save errors). Extracted from
// ResearchWorkspace.tsx to keep the workspace file a thin layout shell.
import { AlertCircle } from 'lucide-react'

export function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="flex items-center gap-1.5 shrink-0 px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
      <AlertCircle className="size-3.5 shrink-0" />
      <span className="truncate">{message}</span>
    </div>
  )
}
