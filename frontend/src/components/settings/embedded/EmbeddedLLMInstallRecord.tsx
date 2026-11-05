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
import type { EmbeddedLLMStatus, EmbeddedLLMGuard } from '@/api/embedded'
import { guardIssueURL, guardIsUnapplied } from '@/lib/embeddedGuardView'
import type { EmbeddedLLMPlan } from '@/api/embeddedTuning'

/** One compatibility decision as a visible line: an UNAPPLIED guard is a
 *  warning (the install works, but not the way the resolver would prefer —
 *  and the guidance says what to do), an applied one is muted context. The
 *  issue citation renders as a link when it has the expected shape. */
function GuardLine({ decision }: { decision: EmbeddedLLMGuard }) {
  const url = guardIssueURL(decision.issue)
  const unapplied = guardIsUnapplied(decision)
  return (
    <p
      className={
        unapplied ? 'text-xs text-warning' : 'text-xs text-muted-foreground'
      }
      data-testid={unapplied ? 'embedded-llm-guard-warning' : 'embedded-llm-guard-applied'}
      title={decision.guidance}
    >
      {decision.guidance}
      {url && (
        <>
          {' '}
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="underline underline-offset-2"
            data-testid="embedded-llm-guard-issue-link"
          >
            {decision.issue}
          </a>
        </>
      )}
    </p>
  )
}

/** MiB with a GiB promotion at the 1024 boundary; whole GiB without decimals. */
function formatMiB(mib: number): string {
  if (mib >= 1024) {
    const gib = mib / 1024
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)} GiB`
  }
  return `${mib} MiB`
}

/** The launch shape as one compact line; '—' when nothing was recorded.
 *  Engine-aware: the mtplx planner prices ONE unified memory pool and has no
 *  fit pass, no `-np` flag and no device/host split (`mlxValidate` rejects the
 *  llama fields) — rendering them under mtplx would describe decisions nobody
 *  made, so the MLX line carries the window and the pool only. The llama
 *  vocabulary stays for the llama family — and for a pre-engine-era record,
 *  which is a llama install. */
function formatPlan(plan: EmbeddedLLMPlan, engine: string): string {
  if (!plan.recorded) return '—'
  if (engine === 'mtplx') {
    return [
      plan.context_size > 0 ? `${plan.context_size} ctx` : null,
      `${formatMiB(plan.expected_device_mib)} pool`,
    ]
      .filter((part) => part !== null && part !== '')
      .join(' · ')
  }
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

/** The human label for the runtime family on disk. `""` is a pre-engine
 *  install — the llama family is what could have written such a record, so the
 *  empty spelling is the ONLY id that renders as llama.cpp by default; any
 *  other unrecognized non-empty id renders as its raw id (the embeddedLLMLabels
 *  convention: never a guess), so a future engine's install record cannot be
 *  mislabeled as llama.cpp. */
function engineLabel(engine: string): string {
  switch (engine) {
    case 'mtplx':
      return 'MLX (MTPLX)'
    case '':
    case 'llama':
      return 'llama.cpp'
    default:
      return engine
  }
}

export function EmbeddedLLMInstallRecord({ status }: { status: EmbeddedLLMStatus }) {
  const items: { label: string; value: string; testId: string }[] = [
    { label: 'Engine', value: engineLabel(status.engine), testId: 'embedded-llm-engine' },
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
      value: formatPlan(status.plan, status.engine),
      testId: 'embedded-llm-plan',
    },
  ]
  // The mtplx install's informational records, beside the sibling fields they
  // belong with: the managed CPython version and the pinned pack revision
  // (abbreviated — the full sha lives in the config record).
  if (status.python_version !== '') {
    items.push({
      label: 'Python',
      value: status.python_version,
      testId: 'embedded-llm-python',
    })
  }
  if (status.pack_revision !== '') {
    items.push({
      label: 'Pack',
      value: status.pack_revision.slice(0, 8),
      testId: 'embedded-llm-pack-revision',
    })
  }

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
      {status.engine === 'mtplx' && (
        // The MTPLX NOTICE attribution obligation (Apache-2.0 §4(d)): a product
        // built on MTPLX says so inside the product, somewhere a user can see.
        // This line is that statement.
        <p className="text-xs text-muted-foreground" data-testid="embedded-llm-attribution">
          Powered by MTPLX —{' '}
          <a
            href="https://github.com/youssofal/MTPLX"
            target="_blank"
            rel="noreferrer"
            className="underline decoration-muted-foreground/40 underline-offset-2 hover:text-foreground"
          >
            github.com/youssofal/MTPLX
          </a>
        </p>
      )}
      {status.guards.length > 0 && (
        <div
          className="flex flex-col gap-0.5"
          data-testid="embedded-llm-guard-records"
        >
          {status.guards.map((decision) => (
            <GuardLine key={`${decision.guard}:${decision.issue}`} decision={decision} />
          ))}
        </div>
      )}
      {status.devices.length > 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="embedded-llm-devices">
          {status.devices
            .map((d) => `${d.name} — ${formatMiB(d.total_mib)}`)
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
