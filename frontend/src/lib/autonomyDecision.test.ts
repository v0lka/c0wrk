import { describe, it, expect } from 'vitest'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { roleToType } from '@/lib/chatUtils'
import { reconstructContent } from '@/lib/chatUtilsHelpers'
import { autonomyDecisionContent } from '@/lib/autonomyDecision'
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
      .toBe('Silent mode (judge): denied bash_exec (runs a shell command) — ASI05: unverified download')
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
      .toBe('Silent mode (allow): allowed write_file — ran unattended: no hard safety reason')
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
})

// ---------------------------------------------------------------------------
// handleAutonomyDecisionEvent — non-blocking notice + reload parity
// ---------------------------------------------------------------------------

describe('handleAutonomyDecisionEvent', () => {
  it('adds a non-blocking status notice carrying the payload in metadata', () => {
    const sessionId = 'sess-silent-1'
    const data: AutonomyDecisionData = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      source: 'core', reason: 'runs a shell command', justification: 'ASI05',
    }
    handleAutonomyDecisionEvent(sessionId, data)

    const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
    const notice = msgs.find(m => m.type === 'status')
    expect(notice).toBeDefined()
    expect(notice!.content).toBe(autonomyDecisionContent(data))
    expect(notice!.metadata).toMatchObject({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
    })
    // Non-blocking: no HITL/pending-action type is produced.
    expect(msgs.some(m => m.type === 'tool_confirm' || m.type === 'step_limit')).toBe(false)
  })

  it('renders an assisted auto-deny as a non-blocking status notice too', () => {
    const sessionId = 'sess-assisted-1'
    const data: AutonomyDecisionData = {
      kind: 'assisted_deny', mode: 'assisted', verdict: 'deny', tool: 'bash_exec',
      source: 'core', reason: 'runs a shell command', justification: 'exfiltration flow confirmed',
    }
    handleAutonomyDecisionEvent(sessionId, data)

    const msgs = selectSessionMessages(useChatStore.getState(), sessionId)
    const notice = msgs.find(m => m.type === 'status')
    expect(notice).toBeDefined()
    expect(notice!.content).toBe('Assisted mode: denied bash_exec (runs a shell command) — exfiltration flow confirmed')
    expect(notice!.metadata).toMatchObject({ kind: 'assisted_deny', mode: 'assisted', verdict: 'deny' })
    // Non-blocking: an assisted auto-deny never opens a pending-action card.
    expect(msgs.some(m => m.type === 'tool_confirm' || m.type === 'step_limit')).toBe(false)
  })

  it('maps the persisted role to the status notice so a reload is identical', () => {
    expect(roleToType.autonomy_decision).toBe('status')

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
