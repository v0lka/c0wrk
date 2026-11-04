// EmbeddedLLMDeviceMemory — the LIVE device-memory panel of the embedded
// local model's Settings block: the accelerator inventory as measured at THIS
// instant, per-device free/total, one refresh button, and the probe's refusal
// when there is no honest answer.
//
// It exists beside the install record's TOPOLOGY (the recorded snapshot the
// status carries) because a recording ages: another process takes the VRAM,
// the weights land and return, a driver updates. The recorded snapshot
// explains the plan (its budgets price total capacity — an occupancy change
// must not move a reproducible plan); this panel shows the machine as it is
// right now. The two never merge: different questions, different snapshots.
//
// The panel is llama-family only: the live probe asks the provisioned
// llama-family binary and the mtplx engine provisions none (its RPC refuses
// by engine), so under `engine: 'mtplx'` the panel renders nothing and both
// automatic probes skip — the install record's recorded topology remains
// that engine's only honest answer.
//
// Data flow follows the store contract: the snapshot lives in
// embeddedLLMStore (`deviceSnapshot`, replaced whole by
// refreshEmbeddedLLMDevices — the writer in embeddedLLMSync), arrives here
// through stable selectors (primitive or direct store reference), and is
// refreshed three ways: automatically on `embedded_llm:state` (a transition
// can move the free memory — the weights landing, an unload returning them),
// automatically once on mount when nothing has been measured yet, and
// manually by the button (mode "manual", the only one that paints a refusal).
//
// Zoom-safety: the rows are plain flex flow and the one derived width is a
// percentage — no viewport units, no pointer-anchored placement. The panel
// flows in the settings column, which is scale-agnostic.

import { useEffect, useMemo, useRef } from 'react'
import { RefreshCw, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { EmbeddedLLMDevice } from '@/api/embeddedTuning'
import {
  refreshEmbeddedLLMDevices,
  useEmbeddedLLMDeviceSnapshot,
  useEmbeddedLLMDeviceSnapshotError,
  useEmbeddedLLMDeviceSnapshotLoading,
} from '@/stores/embeddedLLMStore'

/** MiB with a GiB promotion at the 1024 boundary; whole GiB without decimals
 *  (the install record's own vocabulary, kept leaf-local like its original). */
function formatMiB(mib: number): string {
  if (mib >= 1024) {
    const gib = mib / 1024
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)} GiB`
  }
  return `${mib} MiB`
}

/** One accelerator row: name and description, the free/total figures, and a
 *  percentage share bar of the FREE fraction. The bar's width is a
 *  percentage of the row — the zoom-safe primitive for a derived length. */
function DeviceRow({ device }: { device: EmbeddedLLMDevice }) {
  const freePct = useMemo(() => {
    if (device.total_mib <= 0) return 0
    return Math.max(0, Math.min(100, Math.round((device.free_mib / device.total_mib) * 100)))
  }, [device.total_mib, device.free_mib])
  return (
    <div className="flex flex-col gap-1" data-testid="embedded-llm-device-row">
      <div className="flex items-baseline gap-2">
        <span className="truncate text-xs font-medium">{device.name}</span>
        <span className="truncate text-xs text-muted-foreground">{device.description}</span>
        <span className="ml-auto shrink-0 text-xs text-muted-foreground" data-testid="embedded-llm-device-memory">
          {formatMiB(device.free_mib)} free of {formatMiB(device.total_mib)}
        </span>
      </div>
      <div className="h-1 w-full overflow-hidden rounded-sm bg-muted" aria-hidden>
        <div
          className={cn('h-full', freePct < 25 ? 'bg-warning' : 'bg-primary/60')}
          style={{ width: `${freePct}%` }}
        />
      </div>
    </div>
  )
}

export function EmbeddedLLMDeviceMemory({ installed, engine }: { installed: boolean; engine?: string }) {
  const snapshot = useEmbeddedLLMDeviceSnapshot()
  const loading = useEmbeddedLLMDeviceSnapshotLoading()
  const error = useEmbeddedLLMDeviceSnapshotError()

  // Undefined engine = the llama family (pre-engine installs).
  const llamaFamily = engine !== 'mtplx'

  // First measurement: the `embedded_llm:state` invalidation only fires on a
  // transition, so a panel opened before the next one would render nothing
  // forever. Probe once on mount (installed llama-family only — the RPC
  // refuses before any install exists and by engine under mtplx) in the quiet
  // `auto` mode; the boolean condition, not the snapshot reference, is the
  // dependency, so a failed probe does not loop.
  const probedOnce = useRef(false)
  useEffect(() => {
    if (!installed || !llamaFamily || probedOnce.current) return
    probedOnce.current = true
    void refreshEmbeddedLLMDevices('auto')
  }, [installed, llamaFamily])

  const refresh = () => {
    void refreshEmbeddedLLMDevices('manual')
  }

  if (!installed || !llamaFamily) return null

  return (
    <div className="flex flex-col gap-2" data-testid="embedded-llm-device-memory-panel">
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">Device memory (live)</span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 gap-1 px-2 text-xs"
          onClick={refresh}
          disabled={loading}
          aria-label="Refresh device memory"
          title="Measure the accelerator memory now"
          data-testid="embedded-llm-device-refresh"
        >
          <RefreshCw className={cn('size-3', loading && 'animate-spin')} aria-hidden />
          Refresh
        </Button>
        {snapshot && (
          <span className="ml-auto text-xs text-muted-foreground">
            measured {new Date(snapshot.probed_at).toLocaleString()}
          </span>
        )}
      </div>

      {error && (
        <p className="text-xs text-warning" role="alert" data-testid="embedded-llm-device-error">
          {error}
        </p>
      )}

      {snapshot && snapshot.devices.length === 0 && (
        <p className="text-xs text-muted-foreground">
          The provisioned runtime reports no accelerator this build can use — the model runs on the
          CPU.
        </p>
      )}

      {snapshot && snapshot.devices.length > 0 && (
        <div className="flex flex-col gap-2" role="group" aria-label="Device memory">
          {snapshot.devices.map((device) => (
            <DeviceRow key={device.name} device={device} />
          ))}
        </div>
      )}

      {snapshot && snapshot.unified && snapshot.devices.length > 0 && (
        <p className="text-xs text-muted-foreground" data-testid="embedded-llm-device-unified">
          <TriangleAlert className="mr-1 inline size-3 align-[-2px]" aria-hidden />
          Unified memory: the device pool aliases system RAM — the two budgets share one pool and
          are never summed.
        </p>
      )}

      {!snapshot && !loading && !error && (
        <p className="text-xs text-muted-foreground">No measurement yet — refresh to probe now.</p>
      )}

      <p className="text-xs text-muted-foreground">
        A live view, not the plan's input: the launch budgets price total capacity, so memory held
        by other processes changes nothing about the shape a load launches — it only decides
        whether that shape fits right now. The plan's own snapshot lives in the install record.
      </p>
    </div>
  )
}
