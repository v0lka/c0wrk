import { describe, it, expect } from 'vitest'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { roleToType } from '@/lib/chatUtils'
import { reconstructContent } from '@/lib/chatUtilsHelpers'
import {
  autonomyDecisionContent,
  autonomyDecisionTitle,
  autonomyDecisionVerdictLine,
  isAutonomyAllowVerdict,
  isCanonicalNetworkFlow,
  networkFlowLabel,
  networkFlowQualifier,
  networkSummary,
  networkDecisionLine,
} from '@/lib/autonomyDecision'
import { handleAutonomyDecisionEvent } from '@/hooks/events/hitlHandlers'
import type { AutonomyDecisionData } from '@/types/events'

// ---------------------------------------------------------------------------
// autonomyDecisionContent — the shared notice text (OWASP ASI10 audit trail)
// ---------------------------------------------------------------------------

describe('autonomyDecisionContent', () => {
  it('renders a silent tool denial with the sub-policy, tool, reason and judge justification', () => {
    const d: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'ASI05: unverified download',
    }
    expect(autonomyDecisionContent(d))
      .toBe('Silent mode («Judge» policy): denied bash_exec (runs a shell command) — ASI05: unverified download')
  })

  it('renders an assisted auto-deny (strict-judge DENY, no card) as a status-notice text', () => {
    const d: AutonomyDecisionData = {
      kind: 'assisted_deny', mode: 'assisted', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'exfiltration flow confirmed',
    }
    expect(autonomyDecisionContent(d))
      .toBe('Assisted mode: denied bash_exec (runs a shell command) — exfiltration flow confirmed')
  })

  it('renders an unattended allow', () => {
    const d: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'allow', verdict: 'allow', tool: 'write_file',
      justification: 'ran unattended: no hard safety reason',
    }
    expect(autonomyDecisionContent(d))
      .toBe('Silent mode («Allow» policy): allowed write_file — ran unattended: no hard safety reason')
  })

  it('labels every sub-policy as a policy so it cannot be misread as the verdict', () => {
    const d: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'deny', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'sub-policy denial — fail-closed',
    }
    expect(autonomyDecisionContent(d))
      .toBe('Silent mode («Deny» policy): denied bash_exec (runs a shell command) — sub-policy denial — fail-closed')
  })

  it('renders a step-limit circuit-breaker decision with the position', () => {
    const d: AutonomyDecisionData = {
      kind: 'step_limit', mode: 'silent', verdict: 'deny', current_step: 4, max_steps: 4,
      category: 'circuit_breaker', justification: 'too many failed retries',
    }
    expect(autonomyDecisionContent(d))
      .toBe('Silent mode: step limit deny at step 4/4 (circuit breaker) — too many failed retries')
  })

  it('renders a plain budget decision without inventing a trigger', () => {
    const d: AutonomyDecisionData = {
      kind: 'step_limit', mode: 'silent', verdict: 'allow_more', current_step: 20, max_steps: 20, category: 'budget',
    }
    expect(autonomyDecisionContent(d)).toBe('Silent mode: step limit allow_more at step 20/20')
  })

  it('appends the network flow, host and operand for a network-touching shell decision', () => {
    const d: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'unverified download',
      network: { flow: 'ingest', canonical: false, hosts: ['example.com'], operands: ['PII-Trace.pdf'] },
    }
    expect(autonomyDecisionContent(d)).toBe(
      'Silent mode («Judge» policy): denied bash_exec (runs a shell command) — unverified download' +
      ' [network: External-content ingest to example.com → PII-Trace.pdf (non-canonical — judge-clearable)]',
    )
  })

  it('renders a clean fetch with no canonicality qualifier', () => {
    const d: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'allow', tool: 'bash_exec',
      network: { flow: 'fetch', hosts: ['example.com'] },
    }
    expect(autonomyDecisionContent(d)).toContain('[network: Network fetch to example.com]')
  })
})

// ---------------------------------------------------------------------------
// Network-decision helpers — the flow/host/operand copy + canonicality
// ---------------------------------------------------------------------------

describe('network-decision helpers', () => {
  it('labels each flow', () => {
    expect(networkFlowLabel('cradle')).toBe('Download cradle')
    expect(networkFlowLabel('ingest')).toBe('External-content ingest')
    expect(networkFlowLabel('fetch')).toBe('Network fetch')
    expect(networkFlowLabel('other')).toBe('Network flow')
  })

  it('distinguishes a canonical cradle from a judge-clearable ingest', () => {
    expect(isCanonicalNetworkFlow('cradle')).toBe(true)
    expect(isCanonicalNetworkFlow('ingest')).toBe(false)
    expect(isCanonicalNetworkFlow('fetch')).toBe(false)
    expect(networkFlowQualifier('cradle')).toBe('canonical — cannot be waived')
    expect(networkFlowQualifier('ingest')).toBe('non-canonical — judge-clearable')
    expect(networkFlowQualifier('fetch')).toBe('')
  })

  it('summarizes a cradle, an ingest and a clean fetch', () => {
    expect(networkSummary({ flow: 'cradle', canonical: true, hosts: ['evil.sh'] }))
      .toBe('Download cradle to evil.sh (canonical — cannot be waived)')
    expect(networkSummary({ flow: 'ingest', hosts: ['example.com'], operands: ['a.pdf', 'b.zip'] }))
      .toBe('External-content ingest to example.com → a.pdf, b.zip (non-canonical — judge-clearable)')
    expect(networkSummary({ flow: 'fetch', hosts: ['api.example.com'] }))
      .toBe('Network fetch to api.example.com')
  })

  it('returns null when the decision carries no network flow', () => {
    expect(networkDecisionLine({ kind: 'tool_confirm', verdict: 'allow', tool: 'write_file' })).toBeNull()
    expect(networkDecisionLine({ kind: 'tool_confirm', verdict: 'deny', tool: 'bash_exec', network: { flow: 'fetch' } }))
      .toBe('Network fetch')
  })
})

// ---------------------------------------------------------------------------
// Card helpers — verdict → tone/title/line (AutonomyDecisionBlock)
// ---------------------------------------------------------------------------

describe('isAutonomyAllowVerdict', () => {
  it('accepts the ALLOW family — allow and every step-limit allow', () => {
    for (const v of ['allow', 'allow_once', 'allow_more', 'allow_always']) {
      expect(isAutonomyAllowVerdict(v)).toBe(true)
    }
  })

  it('rejects deny and unknown/empty verdicts', () => {
    for (const v of ['deny', 'unknown', '']) {
      expect(isAutonomyAllowVerdict(v)).toBe(false)
    }
  })
})

describe('autonomyDecisionTitle', () => {
  it('names a tool-call gate for tool_confirm and assisted_deny', () => {
    expect(autonomyDecisionTitle({ kind: 'tool_confirm', verdict: 'deny' })).toBe('Tool Call (auto)')
    expect(autonomyDecisionTitle({ kind: 'assisted_deny', verdict: 'deny' })).toBe('Tool Call (auto)')
  })

  it('names a step-limit boundary for step_limit', () => {
    expect(autonomyDecisionTitle({ kind: 'step_limit', verdict: 'allow_more' })).toBe('Step Limit (auto)')
  })
})

describe('autonomyDecisionVerdictLine', () => {
  it('renders an allowed tool call against its tool', () => {
    expect(autonomyDecisionVerdictLine({ kind: 'tool_confirm', verdict: 'allow', tool: 'write_file' }))
      .toBe('Allowed: write_file')
  })

  it('renders a denied tool call against its tool', () => {
    expect(autonomyDecisionVerdictLine({ kind: 'assisted_deny', verdict: 'deny', tool: 'bash_exec' }))
      .toBe('Denied: bash_exec')
  })

  it('renders a step-limit decision with its position', () => {
    expect(autonomyDecisionVerdictLine({ kind: 'step_limit', verdict: 'allow_more', current_step: 20, max_steps: 20 }))
      .toBe('Step limit: allow_more at step 20/20')
  })
})

// ---------------------------------------------------------------------------
// handleAutonomyDecisionEvent — non-blocking notice + reload parity
// ---------------------------------------------------------------------------

describe('handleAutonomyDecisionEvent', () => {
  it('adds a non-blocking autonomy_decision notice carrying the payload in metadata', () => {
    const sessionId = 'sess-silent-1'
    const data: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      source: 'core', reason: 'runs a shell command', justification: 'ASI05',
    }
    handleAutonomyDecisionEvent(sessionId, data)

    const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
    const notice = msgs.find(m => m.type === 'autonomy_decision')
    expect(notice).toBeDefined()
    expect(notice!.content).toBe(autonomyDecisionContent(data))
    expect(notice!.metadata).toMatchObject({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
    })
    // Non-blocking: no HITL/pending-action type is produced.
    expect(msgs.some(m => m.type === 'tool_confirm' || m.type === 'step_limit')).toBe(false)
  })

  it('renders an assisted auto-deny as a non-blocking autonomy_decision notice too', () => {
    const sessionId = 'sess-assisted-1'
    const data: AutonomyDecisionData = {
      kind: 'assisted_deny', mode: 'assisted', verdict: 'deny', tool: 'bash_exec',
      source: 'core', reason: 'runs a shell command', justification: 'exfiltration flow confirmed',
    }
    handleAutonomyDecisionEvent(sessionId, data)

    const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
    const notice = msgs.find(m => m.type === 'autonomy_decision')
    expect(notice).toBeDefined()
    expect(notice!.content).toBe('Assisted mode: denied bash_exec (runs a shell command) — exfiltration flow confirmed')
    expect(notice!.metadata).toMatchObject({ kind: 'assisted_deny', mode: 'assisted', verdict: 'deny' })
    // Non-blocking: an assisted auto-deny never opens a pending-action card.
    expect(msgs.some(m => m.type === 'tool_confirm' || m.type === 'step_limit')).toBe(false)
  })

  it('maps the persisted role to its own autonomy_decision notice so a reload is identical', () => {
    expect(roleToType.autonomy_decision).toBe('autonomy_decision')

    const data: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', verdict: 'deny', tool: 'bash_exec', justification: 'ASI05',
    }
    // Reload path: the persisted row's metadata is rebuilt into the same text
    // the live handler produced.
    expect(reconstructContent('autonomy_decision', JSON.stringify(data), data as unknown as Record<string, unknown>))
      .toBe(autonomyDecisionContent(data))
  })

  it('falls back to raw content when metadata is not an autonomy-decision payload', () => {
    expect(reconstructContent('autonomy_decision', 'legacy', {})).toBe('legacy')
  })
})
