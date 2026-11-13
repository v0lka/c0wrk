import { AlertTriangle, CheckCircle2, Cpu, HelpCircle, Info } from 'lucide-react'
import type { VectorIndexStatus } from '@/types/models'

/**
 * Short display labels for provider wire values. "auto" can appear only as
 * the requested value — the effective provider is always resolved ("cpu",
 * "cuda" or "openvino") by the time the embedder exists.
 */
const PROVIDER_LABELS: Record<string, string> = {
  auto: 'Auto',
  cpu: 'CPU',
  cuda: 'CUDA',
  openvino: 'OpenVINO',
}

function providerLabel(value: string | undefined): string {
  return value ? PROVIDER_LABELS[value] ?? value : '—'
}

const BADGE_VERIFIED = 'border-success/40 bg-success/10 text-success'
const BADGE_UNVERIFIED = 'border-warning/40 bg-warning/10 text-warning'

/**
 * How the requested → effective pair is classified for rendering. "auto"
 * ALWAYS diverges from the effective provider (it is resolved once, at
 * embedder creation), so raw inequality cannot mean "fallback":
 *
 * - 'cuda-fallback' — an explicit "cuda" request degraded to "cpu": the one
 *   true CUDA fallback. The backend guarantees a non-empty
 *   provider_fallback_reason on this path (WARN + runtime_error toast).
 * - 'openvino-fallback' — an explicit "openvino" request degraded to
 *   "cpu": the OpenVINO twin of the CUDA fallback (plugin missing or init
 *   failure); same non-empty-reason guarantee.
 * - 'auto-cpu' — "auto" resolved to "cpu": at INIT time this is the
 *   expected degradation path of Auto (no hardware provider usable;
 *   legitimate, not an error; the concrete cause is WARN-logged at startup
 *   and is not part of the status payload — the empty-reason info note).
 *   The same pair WITH a non-empty provider_fallback_reason is a runtime
 *   slide (sp4rk ADR-011: the hardware winner died under load and the
 *   embedder slid to the CPU mid-flight) — the reason carries the runtime
 *   cause, and the note renders as the warning runtime-slide variant.
 * - 'honored' — everything else, including "auto" resolving to "cuda" or
 *   "openvino" (Auto's best outcomes — a success, never a fallback).
 */
type ProviderOutcome = 'honored' | 'auto-cpu' | 'cuda-fallback' | 'openvino-fallback'

function classifyOutcome(
  requested: string | undefined,
  effective: string | undefined,
): ProviderOutcome {
  if (effective !== 'cpu') return 'honored'
  if (requested === 'cuda') return 'cuda-fallback'
  if (requested === 'openvino') return 'openvino-fallback'
  if (requested === 'auto') return 'auto-cpu'
  return 'honored'
}

const EFFECTIVE_COLOR: Record<ProviderOutcome, string> = {
  honored: 'text-success',
  'auto-cpu': 'text-info',
  'cuda-fallback': 'text-warning',
  'openvino-fallback': 'text-warning',
}

/**
 * Read-only runtime diagnostics for the vector-index embedder, fed from
 * `useVectorIndexStore.status` (populated by `vector_index:status` events):
 *
 * - requested → effective provider, colored by outcome (see
 *   classifyOutcome): success when honored (incl. "auto" winning with CUDA
 *   or OpenVINO), info for the expected init-time Auto→CPU degradation,
 *   warning for a real explicit-"cuda"/"openvino"→CPU fallback AND for a
 *   runtime auto→CPU slide (a non-empty provider_fallback_reason on the
 *   auto→cpu pair — sp4rk ADR-011);
 * - the fallback reason callout (explicit "cuda"/"openvino" fallbacks only —
 *   a runtime auto→CPU slide renders its cause in the dedicated
 *   runtime-slide note instead, and the init-time ladder loss carries no
 *   reason at all);
 * - the `cuda_verified` badge: the external nvidia-smi verdict, present only
 *   when the effective provider is CUDA and the startup probe ran;
 * - the `ov_device` badge: the xpu-smi device-class verdict for an OpenVINO
 *   embedder ("gpu" = Intel GPU activity seen, "cpu" = probe ran and saw no
 *   GPU activity), present only when the probe actually answered;
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
  const outcome = classifyOutcome(requested, effective)
  const showCudaBadge = effective === 'cuda' && typeof status.cuda_verified === 'boolean'
  const verified = status.cuda_verified === true
  // The OpenVINO device-class badge renders only when the xpu-smi probe
  // actually answered ("gpu"|"cpu"): an absent ov_device is "unverified,
  // not negative" (xpu-smi missing/failed/timeout), so it renders nothing —
  // mirroring how an absent cuda_verified hides the CUDA badge.
  const ovDevice = status.ov_device
  const showOvBadge =
    effective === 'openvino' && (ovDevice === 'gpu' || ovDevice === 'cpu')
  const ovVerified = ovDevice === 'gpu'
  // A runtime auto→CPU slide (sp4rk ADR-011) is a FAILURE the user must
  // notice — the effective-provider text joins the explicit-provider
  // fallbacks in the warning color; the expected INIT-time Auto→CPU
  // degradation stays the calm info color. See classifyOutcome's doc.
  const runtimeSlide = outcome === 'auto-cpu' && !!reason
  const effectiveColor = runtimeSlide ? 'text-warning' : EFFECTIVE_COLOR[outcome]

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
            <span className={effectiveColor}>{providerLabel(effective)}</span>
          </div>

          <div data-testid="vector-diagnostics-device" className="text-xs text-muted-foreground">
            Device: {status.device_id ?? 0}
          </div>

          {(outcome === 'cuda-fallback' || outcome === 'openvino-fallback') && (
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

          {outcome === 'auto-cpu' && reason && (
            <div
              data-testid="vector-runtime-slide-note"
              className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/10 p-2 text-xs text-warning"
            >
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                The hardware provider won at startup but started failing under load, and the
                embedder slid to the CPU at runtime — embeddings keep working, but slower.
                Cause: {reason} Restart the app (or toggle vector indexing off and on) to retry
                the hardware provider once it is healthy.
              </span>
            </div>
          )}

          {outcome === 'auto-cpu' && !reason && (
            <div
              data-testid="vector-auto-cpu-note"
              className="flex items-start gap-2 rounded-md border border-info/30 bg-info/10 p-2 text-xs text-info"
            >
              <Info className="mt-0.5 size-3.5 shrink-0" />
              <span>
                Auto resolved to the CPU provider — no hardware provider (CUDA, OpenVINO) is
                usable, so embeddings run on CPU. This is the expected Auto behavior, not an
                error; the concrete cause is WARN-logged at startup (see the app logs in
                ~/.c0wrk/logs/).
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

          {showOvBadge && (
            <div
              data-testid="vector-ov-device-badge"
              className={`inline-flex w-fit items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium ${
                ovVerified ? BADGE_VERIFIED : BADGE_UNVERIFIED
              }`}
            >
              {ovVerified ? (
                <CheckCircle2 className="size-3.5 shrink-0" />
              ) : (
                <HelpCircle className="size-3.5 shrink-0" />
              )}
              {ovVerified
                ? 'Intel GPU verified'
                : 'No Intel GPU activity — OpenVINO is on a CPU device'}
            </div>
          )}
        </>
      )}
    </div>
  )
}
