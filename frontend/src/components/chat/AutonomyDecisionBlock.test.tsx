// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { AutonomyDecisionBlock } from './AutonomyDecisionBlock'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import type { ChatMessage } from '@/types/models'
import { chatMessageToUI } from '@/lib/chatUtils'

type AutonomyItem = Extract<DisplayItem, { kind: 'autonomy_decision' }>

function makeItem(metadata: Record<string, unknown> | undefined, content = 'notice text'): AutonomyItem {
  const message: ChatMessageUI = {
    id: 'ad-1',
    sessionId: 'sess-1',
    type: 'autonomy_decision',
    content,
    metadata,
    timestamp: 0,
  }
  return { kind: 'autonomy_decision', message }
}

describe('AutonomyDecisionBlock', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (item: AutonomyItem) =>
    act(() => {
      root.render(<AutonomyDecisionBlock item={item} />)
    })

  // SVG className is an SVGAnimatedString — read the class attribute instead.
  const svgClasses = () =>
    Array.from(container.querySelectorAll('svg')).map(el => el.getAttribute('class') ?? '')
  const card = () => container.firstElementChild as HTMLElement

  it('paints a green Check and the tool-call title for an allow verdict', () => {
    render(makeItem({ kind: 'tool_confirm', mode: 'silent', policy: 'allow', verdict: 'allow', tool: 'write_file' }))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-check')
    expect(cls).not.toContain('lucide-x')
    expect(cls).toContain('text-success')
    expect(cls).not.toContain('text-destructive')
    expect(container.textContent).toContain('Tool Call (auto)')
  })

  it('paints a red X for a deny verdict', () => {
    render(makeItem({ kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec' }))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-x')
    expect(cls).not.toContain('lucide-check')
    expect(cls).toContain('text-destructive')
    expect(cls).not.toContain('text-success')
  })

  it('treats a step-limit allow_* verdict as success and titles it "Step Limit (auto)"', () => {
    render(makeItem({ kind: 'step_limit', mode: 'silent', verdict: 'allow_more', current_step: 20, max_steps: 20 }))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-check')
    expect(cls).toContain('text-success')
    expect(container.textContent).toContain('Step Limit (auto)')
    expect(container.textContent).not.toContain('Tool Call (auto)')
  })

  it('titles an assisted auto-deny as a tool-call gate', () => {
    render(makeItem({ kind: 'assisted_deny', mode: 'assisted', verdict: 'deny', tool: 'bash_exec' }))
    expect(container.textContent).toContain('Tool Call (auto)')
  })

  it('renders the verdict line plus Mode/Policy/Reason/Justification rows when present', () => {
    render(makeItem({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'ASI05: unverified download',
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('Denied: bash_exec')
    expect(text).toContain('Mode:')
    expect(text).toContain('silent')
    expect(text).toContain('Policy:')
    expect(text).toContain('judge')
    expect(text).toContain('Reason:')
    expect(text).toContain('runs a shell command')
    expect(text).toContain('Justification:')
    expect(text).toContain('ASI05: unverified download')
  })

  it('omits rows whose fields are absent', () => {
    render(makeItem({ kind: 'assisted_deny', mode: 'assisted', verdict: 'deny', tool: 'bash_exec' }))
    const text = container.textContent ?? ''
    expect(text).toContain('Mode:')
    expect(text).not.toContain('Policy:')
    expect(text).not.toContain('Reason:')
    expect(text).not.toContain('Justification:')
  })

  it('uses the same card chrome as the standard card', () => {
    render(makeItem({ kind: 'tool_confirm', mode: 'silent', verdict: 'allow', tool: 'write_file' }))
    const cls = card().className
    expect(cls).toContain('rounded-md')
    expect(cls).toContain('border')
    expect(cls).toContain('px-3')
    expect(cls).toContain('py-2')
    expect(cls).toContain('border-success/30')
    expect(cls).toContain('bg-success/5')
  })

  it('renders a muted AlertTriangle fallback on malformed metadata without throwing', () => {
    // A corrupted/foreign payload whose `verdict` is not a string must not reach
    // `verdict.startsWith` — the boundary guard rejects it and the card falls
    // back to a neutral muted notice.
    render(makeItem({ kind: 'tool_confirm', verdict: 123 }, 'Raw persisted notice'))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-triangle-alert')
    expect(cls).toContain('text-muted-foreground')
    expect(cls).not.toContain('text-success')
    expect(cls).not.toContain('text-destructive')
    expect(container.textContent).toContain('Raw persisted notice')
  })

  it('renders the fallback on undefined metadata without throwing', () => {
    render(makeItem(undefined))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-triangle-alert')
    expect(container.textContent).toContain('Autonomy Decision')
  })

  it('reload parity: a persisted row decoded from the DB byte array renders the same card', () => {
    // Faithful reload: the persister stores the payload as JSON; Wails delivers
    // metadata as a byte array. chatMessageToUI decodes + parses it, and the
    // card must render the same verdict icon/tone, title and audit rows as the
    // live-built item — the audit trail must not change across a reload.
    const data = {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      reason: 'runs a shell command', justification: 'ASI05: unverified download',
    }
    const json = JSON.stringify(data)
    const persisted: ChatMessage = {
      id: 7,
      session_id: 'sess-1',
      role: 'autonomy_decision',
      content: json,
      metadata: Array.from(new TextEncoder().encode(json)),
      created_at: '2026-01-01T00:00:00Z',
    }
    const ui = chatMessageToUI(persisted)
    render({ kind: 'autonomy_decision', message: ui })

    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-x')
    expect(cls).toContain('text-destructive')
    const text = container.textContent ?? ''
    expect(text).toContain('Tool Call (auto)')
    expect(text).toContain('Denied: bash_exec')
    expect(text).toContain('ASI05: unverified download')
  })

  it('renders a canonical cradle network flow in the destructive tone', () => {
    render(makeItem({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      network: { flow: 'cradle', canonical: true, hosts: ['evil.sh'] },
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('Network:')
    expect(text).toContain('Download cradle to evil.sh')
    expect(text).toContain('canonical — cannot be waived')
    expect(container.querySelector('.text-destructive.font-medium')).not.toBeNull()
    expect(container.querySelector('.text-warning.font-medium')).toBeNull()
  })

  it('renders a non-canonical ingest in the warning tone, visually distinct from a cradle', () => {
    render(makeItem({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
      network: { flow: 'ingest', canonical: false, hosts: ['example.com'], operands: ['p.pdf'] },
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('External-content ingest to example.com → p.pdf')
    expect(text).toContain('non-canonical — judge-clearable')
    const netLine = container.querySelector('.text-warning.font-medium')
    expect(netLine).not.toBeNull()
    expect(netLine!.textContent).toContain('judge-clearable')
    // The judge-clearable ingest must not read like a canonical control.
    expect(container.querySelector('.text-destructive.font-medium')).toBeNull()
  })

  it('renders a clean fetch in the muted tone with no canonicality qualifier', () => {
    render(makeItem({
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'allow', tool: 'bash_exec',
      network: { flow: 'fetch', hosts: ['example.com'] },
    }))
    const text = container.textContent ?? ''
    expect(text).toContain('Network fetch to example.com')
    expect(text).not.toContain('canonical')
    expect(container.querySelector('.text-warning.font-medium')).toBeNull()
    expect(container.querySelector('.text-destructive.font-medium')).toBeNull()
  })

  it('falls back to the muted card on a malformed network payload', () => {
    render(makeItem({ kind: 'tool_confirm', verdict: 'allow', tool: 'bash_exec', network: { flow: 123 } }, 'Raw notice'))
    const cls = svgClasses()[0] ?? ''
    expect(cls).toContain('lucide-triangle-alert')
    expect(cls).toContain('text-muted-foreground')
    expect(container.textContent).toContain('Raw notice')
  })
})
