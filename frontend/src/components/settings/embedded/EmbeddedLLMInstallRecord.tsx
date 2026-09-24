// The informational install record of the embedded local model.
//
// Purely a LABEL: it reports what the resolver actually chose for THIS machine
// and froze in the manifest — the ternary packing (PQ2_0 | PTQ1_0), the
// EFFECTIVE backend (never the raw probed value: recording that would describe
// an install that is not on disk), the RAM-tiered context, the persisted
// loopback port and the endpoint/provider identity derived from it. Nothing
// here is editable; it exists so the user can see which quantization and
// endpoint they got (specs/domains/embedded-llm.md § Hardware probe →
// resolution, § Settings block).

import { Cpu } from 'lucide-react'
import type { EmbeddedLLMStatus } from '@/api/embedded'

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
      {status.base_url && (
        <p className="truncate text-xs text-muted-foreground" data-testid="embedded-llm-base-url">
          {status.base_url}
          {status.model_id ? ` · ${status.model_id}` : ''}
        </p>
      )}
    </div>
  )
}
