// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
}))

vi.mock('@/api/config', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/config')>()),
  getConfig: mocks.getConfig,
}))

import { ConfigWarningBanner } from './ConfigWarningBanner'
import type { ConfigResponse } from '@/types/models'

const COMPOSITE_ERROR =
  'config.Load: config validation failed: default_model "claude-3-haiku" must be a full "provider/model" identifier — bare model names are not accepted (the model name itself may contain "/": the id splits on the first "/" only)'

function makeConfigResponse(errors: string[], llm: ConfigResponse['llm']): ConfigResponse {
  return {
    loaded: true,
    log_level: 'info',
    config_errors: errors,
    llm,
    search: { provider: '', api_key: '', base_url: '' },
  } as ConfigResponse
}

function makeLLM(anthropicModels: string[]): ConfigResponse['llm'] {
  return {
    default_model: '',
    anthropic: { api_key: '', models: anthropicModels },
    openai_compatible: {},
    anthropic_compatible: {},
    chatgpt: { api_key: '', models: [] },
    kimi: { api_key: '', models: [] },
    all_models: [],
    models_ready: false,
    auto_retry_max_seconds: 300,
  } as ConfigResponse['llm']
}

let container: HTMLDivElement
let root: Root

async function renderBanner(errors: string[], llm: ConfigResponse['llm']): Promise<void> {
  mocks.getConfig.mockResolvedValue(makeConfigResponse(errors, llm))
  act(() => {
    root.render(<ConfigWarningBanner />)
  })
  // Flush the mount effect's getConfig round trip.
  await act(async () => {
    await Promise.resolve()
  })
}

describe('ConfigWarningBanner composite default_model hint', () => {
  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    mocks.getConfig.mockReset()
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
  })

  it('renders the concrete corrected line when a configured provider serves the bare model', async () => {
    await renderBanner([COMPOSITE_ERROR], makeLLM(['claude-3-haiku']))
    const text = container.textContent ?? ''
    // The raw load error stays visible…
    expect(text).toContain(COMPOSITE_ERROR)
    // …plus the ready-to-paste composite form resolved from the provider list.
    expect(text).toContain('default_model: anthropic/claude-3-haiku')
  })

  it('falls back to the provider pattern when no configured provider serves the bare model', async () => {
    await renderBanner([COMPOSITE_ERROR], makeLLM(['claude-sonnet-4']))
    expect(container.textContent ?? '').toContain('default_model: <provider>/claude-3-haiku')
  })

  it('renders no hint for unrelated load errors', async () => {
    const unrelated = 'web_access bind 0.0.0.0 is not loopback'
    await renderBanner([unrelated], makeLLM([]))
    const text = container.textContent ?? ''
    expect(text).toContain(unrelated)
    expect(text).not.toContain('default_model:')
  })
})
