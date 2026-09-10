import { AlertTriangle, CheckCircle2, Cpu, HelpCircle } from 'lucide-react'
import type { VectorIndexStatus } from '@/types/models'

/**
 * Short display labels for provider wire values. "auto" can appear only as
 * the requested value — the effective provider is always resolved ("cpu" or
 * "cuda") by the time the embedder exists.
 */
const PROVIDER_LABELS: Record<string, string> = {
  auto: 'Auto',
  cpu: 'CPU',
  cuda: 'CUDA',
}

function providerLabel(value: string | undefined): string {
  return value ? PROVIDER_LABELS[value] ?? value : '—'
}

const BADGE_VERIFIED = 'border-success/40 bg-success/10 text-success'
const BADGE_UNVERIFIED = 'border-warning/40 bg-warning/10 text-warning'

/**
 * Read-only runtime diagnostics for the vector-index embedder, fed from
 * `useVectorIndexStore.status` (populated by `vector_index:status` events):
 *
 * - requested → effective provider — they differ exactly when a CUDA→CPU
 *   fallback happened at embedder creation;
 * - the fallback reason (CUDA init failure text from the backend);
 * - the `cuda_verified` badge: the external nvidia-smi verdict, present only
 *   when the effective provider is CUDA and the startup probe ran;
 * - the ONNX device index the running embedder was created with.
 *
 * Absent `execution_provider` means no embedder exists (model files missing
 * or creation failed) — the block degrades to a muted note instead of
 * guessing.
 */
export function VectorIndexDiagnostics({ status }: { status: VectorIndexStatus }) {
  const requested = status.requested_execution_provider
  const effective = status.execution_provider
  const reason = status.provider_fallback_reason?.trim()
  const fallbackHappened =
    typeof requested === 'string' && typeof effective === 'string' && requested !== effective
  const showCudaBadge = effective === 'cuda' && typeof status.cuda_verified === 'boolean'
  const verified = status.cuda_verified === true

  return (
    <div
      data-testid="vector-diagnostics"
      className="flex flex-col gap-2 rounded-md border border-border p-3"
    >
      <div className="flex items-center gap-2">
        <Cpu className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium text-muted-foreground">Runtime</span>
      </div>

      {typeof effective !== 'string' ? (
        <p data-testid="vector-diagnostics-idle" className="text-xs text-muted-foreground">
          Embedder is not running — provider details appear once the index starts.
        </p>
      ) : (
        <>
          <div
            data-testid="vector-diagnostics-provider"
            className="flex flex-wrap items-center gap-1.5 text-xs"
          >
            <span className="text-muted-foreground">Provider:</span>
            <span>{providerLabel(requested)}</span>
            <span aria-hidden="true">→</span>
            <span className={fallbackHappened ? 'text-warning' : 'text-success'}>
              {providerLabel(effective)}
            </span>
          </div>

          <div data-testid="vector-diagnostics-device" className="text-xs text-muted-foreground">
            Device: {status.device_id ?? 0}
          </div>

          {fallbackHappened && (
            <div
              data-testid="vector-fallback-reason"
              className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/10 p-2 text-xs text-warning"
            >
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                Fell back to {providerLabel(effective)}: {reason || 'unknown reason'}
              </span>
            </div>
          )}

          {showCudaBadge && (
            <div
              data-testid="vector-cuda-verified-badge"
              className={`inline-flex w-fit items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium ${
                verified ? BADGE_VERIFIED : BADGE_UNVERIFIED
              }`}
            >
              {verified ? (
                <CheckCircle2 className="size-3.5 shrink-0" />
              ) : (
                <HelpCircle className="size-3.5 shrink-0" />
              )}
              {verified ? 'CUDA verified' : 'CUDA not verified by driver probe'}
            </div>
          )}
        </>
      )}
    </div>
  )
}
