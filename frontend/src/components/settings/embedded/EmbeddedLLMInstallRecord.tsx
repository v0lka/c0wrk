// The informational install record of the embedded local model.
//
// Purely a LABEL: it reports what the resolver actually chose for THIS machine
// and froze in the manifest — the ternary packing (PQ2_0 | PTQ1_0), the
// EFFECTIVE backend (never the raw probed value: recording that would describe
// an install that is not on disk), the RAM-tiered context, the persisted
// loopback port and the endpoint/provider identity derived from it — plus the
// MEASURED topology (devices + the unified-memory flag) and the EFFECTIVE plan
// with its planner `Notes` trail, both from the T10 additive status fields.
// Nothing here is editable; it exists so the user can see which quantization,
// which launch shape and which endpoint they got (specs/domains/embedded-llm.md
// § Hardware probe → resolution, § Settings block).

import { Cpu } from 'lucide-react'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMPlan } from '@/api/embeddedTuning'

/** MiB with a GiB promotion at the 1024 boundary; whole GiB without decimals. */
function formatMiB(mib: number): string {
  if (mib >= 1024) {
    const gib = mib / 1024
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)} GiB`
  }
  return `${mib} MiB`
}

/** The launch shape as one compact line; '—' when nothing was recorded. */
function formatPlan(plan: EmbeddedLLMPlan): string {
  if (!plan.recorded) return '—'
  const offload =
    plan.offload_mode === 'layers' && plan.layers >= 0
      ? `${plan.layers} layers`
      : plan.offload_mode
  return [
    plan.packing || null,
    plan.kv_type ? `KV ${plan.kv_type}` : null,
    plan.context_size > 0 ? `${plan.context_size} ctx` : null,
    offload || null,
    plan.fit ? 'fit' : 'fit off',
    `np ${plan.parallel}`,
    `${formatMiB(plan.expected_device_mib)} device / ${formatMiB(plan.expected_host_mib)} host`,
  ]
    .filter((part) => part !== null && part !== '')
    .join(' · ')
}

export function EmbeddedLLMInstallRecord({ status }: { status: EmbeddedLLMStatus }) {
  const items: { label: string; value: string; testId: string }[] = [
    { label: 'Packing', value: status.packing || '—', testId: 'embedded-llm-packing' },
    { label: 'Backend', value: status.backend || '—', testId: 'embedded-llm-backend' },
    {
      label: 'Context',
      value: status.context_size > 0 ? String(status.context_size) : '—',
      testId: 'embedded-llm-context',
    },
    {
      label: 'Port',
      value: status.port > 0 ? String(status.port) : '—',
      testId: 'embedded-llm-port',
    },
    {
      label: 'Unified memory',
      value: status.unified ? 'Yes' : 'No',
      testId: 'embedded-llm-unified',
    },
    {
      label: 'Plan',
      value: formatPlan(status.plan),
      testId: 'embedded-llm-plan',
    },
  ]

  return (
    <div
      className="flex flex-col gap-1.5 rounded-md border border-border bg-muted/30 p-3"
      data-testid="embedded-llm-install-record"
    >
      <div className="flex items-center gap-2">
        <Cpu className="size-4 shrink-0 text-muted-foreground" />
        <span className="text-sm font-medium text-foreground">Installed build</span>
        {status.runtime_version && (
          <span className="truncate text-xs text-muted-foreground">{status.runtime_version}</span>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        {items.map((item) => (
          <span key={item.label} className="text-muted-foreground">
            {item.label}{' '}
            <span className="text-foreground" data-testid={item.testId}>
              {item.value}
            </span>
          </span>
        ))}
      </div>
      {status.devices.length > 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="embedded-llm-devices">
          {status.devices
            .map((d) => `${d.name} — ${formatMiB(d.free_mib)} free of ${formatMiB(d.total_mib)}`)
            .join(' · ')}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground" data-testid="embedded-llm-devices">
          No device topology measured{status.topology_probed_at ? '' : ' (no probe has answered yet)'}.
        </p>
      )}
      {status.plan.recorded && status.plan.notes.length > 0 && (
        <ul
          className="flex flex-col gap-0.5 text-xs text-muted-foreground"
          data-testid="embedded-llm-plan-notes"
        >
          {status.plan.notes.map((note) => (
            <li key={note}>{note}</li>
          ))}
        </ul>
      )}
      {status.fit_warning && (
        <p
          className="text-xs text-warning"
          data-testid="embedded-llm-fit-warning"
          title={status.fit_warning}
        >
          {status.fit_warning}
        </p>
      )}
      {status.base_url && (
        <p className="truncate text-xs text-muted-foreground" data-testid="embedded-llm-base-url">
          {status.base_url}
          {status.model_id ? ` · ${status.model_id}` : ''}
        </p>
      )}
    </div>
  )
}
