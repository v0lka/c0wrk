// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SecurityGroupCard } from './SecurityGroupCard'

let container: HTMLDivElement
let root: Root

const BASE_PATTERN = 'rm\\s+-rf' // one backslash, like a real blocklist entry

beforeEach(() => {
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

const render = (props: Partial<Parameters<typeof SecurityGroupCard>[0]> = {}) =>
  act(async () => {
    await root.render(
      <SecurityGroupCard
        group="execute"
        policy="user_confirm"
        blocklist={[BASE_PATTERN]}
        tools={[{ name: 'bash_exec', description: 'Runs a shell command', source: 'core', group: 'execute', policy: 'user_confirm' }]}
        onPolicyChange={vi.fn()}
        onBlocklistChange={vi.fn()}
        {...props}
      />,
    )
  })

const input = () => {
  const el = container.querySelector<HTMLInputElement>('input[aria-label="New blocklist pattern"]')
  if (!el) throw new Error('pattern input not found')
  return el
}

const typePattern = async (pattern: string) => {
  await act(async () => {
    // React's controlled-input value tracker swallows plain .value
    // assignments; go through the native setter so the input event is
    // seen as a real user edit.
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(input(), pattern)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

const addViaClick = async (pattern: string) => {
  await typePattern(pattern)
  const btn = container.querySelector<HTMLButtonElement>('button[aria-label="Add blocklist pattern"]')
  if (!btn) throw new Error('add button not found')
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

const addViaEnter = async (pattern: string) => {
  await typePattern(pattern)
  await act(async () => {
    input().dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
  })
}

describe('SecurityGroupCard — blocklist editor', () => {
  it('adds a pattern via the button and clears the input', async () => {
    const onBlocklistChange = vi.fn()
    await render({ onBlocklistChange })
    await addViaClick('mkfs')
    expect(onBlocklistChange).toHaveBeenCalledWith('execute', [BASE_PATTERN, 'mkfs'])
    expect(input().value).toBe('')
  })

  it('adds a pattern via Enter and prevents the default form submit', async () => {
    const onBlocklistChange = vi.fn()
    await render({ onBlocklistChange })
    await addViaEnter('dd\\s+if=')
    expect(onBlocklistChange).toHaveBeenCalledWith('execute', [BASE_PATTERN, 'dd\\s+if='])
    expect(input().value).toBe('')
  })

  it('rejects a duplicate pattern without calling onChange', async () => {
    const onBlocklistChange = vi.fn()
    await render({ onBlocklistChange })
    await addViaClick(BASE_PATTERN)
    expect(onBlocklistChange).not.toHaveBeenCalled()
    expect(input().value).toBe('')
  })

  it('removes a pattern via its chip button', async () => {
    const onBlocklistChange = vi.fn()
    await render({ onBlocklistChange })
    // Prefix-match: the full aria-label embeds the pattern text, which can
    // contain characters hostile to CSS attribute-selector escaping.
    const chip = container.querySelector<HTMLButtonElement>('button[aria-label^="Remove pattern "]')
    if (!chip) throw new Error('remove button not found')
    await act(async () => {
      chip.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onBlocklistChange).toHaveBeenCalledWith('execute', [])
  })

  it('blocks a non-compiling regex with an inline error and does not call onChange', async () => {
    const onBlocklistChange = vi.fn()
    await render({ onBlocklistChange })
    // Unmatched parenthesis — rejected by both the JS pre-flight and Go's
    // regexp.Compile, so the inline hint and the backend gate agree.
    await addViaClick('(unclosed')
    expect(onBlocklistChange).not.toHaveBeenCalled()
    const alert = container.querySelector('[role="alert"]')
    expect(alert?.textContent).toMatch(/Invalid regular expression/)
    // The offending text stays in the input for correction.
    expect(input().value).toBe('(unclosed')
    // Typing anything else clears the error (a different value, so the
    // controlled input actually fires onChange).
    await typePattern('(unclosed!')
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('renders the editor only for the execute group', async () => {
    await render({
      group: 'local_write',
      tools: [{ name: 'write_file', description: 'Writes a file', source: 'core', group: 'local_write', policy: 'user_confirm' }],
    })
    expect(container.textContent ?? '').not.toContain('Blocklist patterns')
    expect(container.querySelector('input[aria-label="New blocklist pattern"]')).toBeNull()
  })

  it('dedupes patterns from a hand-edited config (unique React keys)', async () => {
    // The backend stores the blocklist verbatim, so a hand-merged config can
    // carry the same pattern twice (ADR-024 §7 migration).
    await render({ blocklist: [BASE_PATTERN, BASE_PATTERN] })
    // One chip, not two duplicate-keyed chips.
    const chips = container.querySelectorAll('button[aria-label^="Remove pattern "]')
    expect(chips).toHaveLength(1)
  })

  it('saves a deduped list when adding to a config that carried duplicates', async () => {
    const onBlocklistChange = vi.fn()
    await render({ blocklist: [BASE_PATTERN, BASE_PATTERN], onBlocklistChange })
    await addViaClick('mkfs')
    expect(onBlocklistChange).toHaveBeenCalledWith('execute', [BASE_PATTERN, 'mkfs'])
  })

  it('renders no restore-defaults affordance (the blocklist is user-authored, empty by default)', async () => {
    await render({ blocklist: [] })
    expect(container.querySelector('button[title^="Replace the list"]')).toBeNull()
    expect(container.textContent ?? '').not.toContain('Restore defaults')
  })
})
