import { useCallback, useEffect, useState } from 'react'
import { Monitor, RefreshCw, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { SegmentedControl } from '@/components/ui/segmented-control'
import {
  getWebviewGpuPolicy,
  setWebviewGpuPolicy,
  type WebviewGpuPolicyInfo,
  type WebviewGpuPolicySetting,
} from '@/api/webview'
import { isLinuxHost } from '@/lib/platform'
import { logger } from '@/lib/logger'

const POLICY_OPTIONS: { value: WebviewGpuPolicySetting; label: string }[] = [
  { value: 'auto', label: 'Auto (recommended)' },
  { value: 'always', label: 'Always' },
  { value: 'on-demand', label: 'On demand' },
  { value: 'never', label: 'Never' },
]

/**
 * Linux-only webview GPU-acceleration setting (issue #34). Auto resolves to
 * hardware acceleration; when the DMABUF renderer fails at startup the
 * watchdog falls back to software rendering automatically and records why —
 * surfaced here with a retry action (re-choosing auto re-arms it).
 */
export function WebviewGpuPolicySettings() {
  const [info, setInfo] = useState<WebviewGpuPolicyInfo | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!isLinuxHost()) return
    getWebviewGpuPolicy()
      .then(setInfo)
      .catch((err) => logger.error('Failed to load webview GPU policy:', err))
  }, [])

  const applySetting = useCallback(async (setting: WebviewGpuPolicySetting) => {
    setBusy(true)
    try {
      const next = await setWebviewGpuPolicy(setting)
      setInfo(next)
    } catch (err) {
      logger.error('Failed to set webview GPU policy:', err)
    } finally {
      setBusy(false)
    }
  }, [])

  // Platform gate: the policy feeds the WebKitGTK frontend only. The backend
  // persists harmlessly on other platforms, but the section stays hidden.
  if (!isLinuxHost()) return null

  return (
    <div className="flex flex-col gap-3" data-testid="webview-gpu-policy-settings">
      <div className="flex items-center gap-2">
        <Monitor className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Webview GPU Acceleration</span>
      </div>
      {/* Radio semantics and the primitive's own compact, content-width track —
          no `fullWidth`/`flex-1`, which stretched the segments and was the
          only chrome difference from Security → Autonomy mode. Each item keeps
          its stable `webview-gpu-*` test hook; the string label doubles as the
          native title (the primitive's default). */}
      <SegmentedControl<WebviewGpuPolicySetting>
        semantic="radio"
        ariaLabel="Webview GPU acceleration"
        disabled={busy}
        items={POLICY_OPTIONS.map((option) => ({
          value: option.value,
          label: option.label,
          testId: `webview-gpu-${option.value}`,
        }))}
        value={info?.setting ?? null}
        onValueChange={(setting) => void applySetting(setting)}
      />
      {info && (
        <p className="text-xs text-muted-foreground" data-testid="webview-gpu-running">
          Rendering now: <span className="text-foreground">{info.running}</span>
          {info.running_source === 'env' && ' (forced by C0WRK_WEBVIEW_GPU_POLICY)'}
          {info.running_source === 'auto-fallback' && ' (automatic fallback)'}
          {info.running_source === 'auto-unobserved' && ' (stderr capture unavailable — no failure detection this run)'}
          {' — '}
          {info.restart_required ? 'restart to apply the new setting.' : 'up to date.'}
        </p>
      )}
      {info?.fallback && (
        <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-2" data-testid="webview-gpu-fallback">
          <TriangleAlert className="h-4 w-4 shrink-0 text-warning" />
          <div className="flex flex-col gap-1 text-xs">
            <span>
              GPU acceleration was disabled automatically after a rendering failure
              ({info.fallback.detected_at}). Software rendering is stable but uses more CPU.
            </span>
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              title="Retry GPU acceleration"
              className="h-6 w-fit gap-1 px-2 text-xs text-foreground"
              data-testid="webview-gpu-retry"
              onClick={() => void applySetting('auto')}
            >
              <RefreshCw className="h-3 w-3" />
              Retry GPU acceleration
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
