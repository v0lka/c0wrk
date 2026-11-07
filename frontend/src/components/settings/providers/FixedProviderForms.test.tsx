// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactNode } from 'react'
import type { ProviderConfig } from './ProviderAccordion'

vi.mock('./ProviderAccordion', () => ({
  ProviderAccordion: ({ authSection }: { authSection?: ReactNode }) => (
    <div data-testid="fixed-accordion">{authSection}</div>
  ),
}))

vi.mock('@/hooks/useKimiAuthFlow', () => ({
  useKimiAuthFlow: () => ({
    status: null,
    signInBusy: false,
    signOutBusy: false,
    authError: null,
    handleSignIn: vi.fn(),
    handleCancel: vi.fn(),
    handleSignOut: vi.fn(),
  }),
}))

import { FixedProviderForms } from './FixedProviderForms'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function renderKimi(type?: 'openai' | 'anthropic'): void {
  const config: ProviderConfig = {
    api_key: 'key',
    base_url: 'https://example.test/v1',
    models: ['model'],
    tls_fingerprint: '',
    type,
  }
  act(() => root.render(
    <FixedProviderForms
      providerConfigs={{ kimi: config }}
      expandedProviders={new Set()}
      onToggle={() => {}}
      onConfigChange={() => {}}
      onToggleModel={() => {}}
      defaultModel=""
      autoRetryMaxSeconds={3600}
    />,
  ))
}

describe('FixedProviderForms legacy Kimi collision', () => {
  it('does not render the fixed accordion for a legacy compatible Kimi', () => {
    renderKimi('openai')
    expect(container.querySelector('[data-testid="fixed-accordion"]')).toBeNull()
  })

  it('keeps the fixed accordion and auth control without a collision', () => {
    renderKimi()
    expect(container.querySelector('[data-testid="fixed-accordion"]')).not.toBeNull()
    expect(container.querySelector('button[aria-label="Kimi authentication mode"]')).not.toBeNull()
  })
})
