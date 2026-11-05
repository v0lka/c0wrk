// @vitest-environment jsdom
//
// The engine line and the MTPLX attribution of the embedded-LLM install
// record. The attribution is a LICENSE obligation (MTPLX NOTICE, Apache-2.0
// §4(d)): a product built on MTPLX says so inside the product. It renders when
// — and only when — the engine on disk is mtplx; the llama family carries no
// such obligation and must not show the line.

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import type { EmbeddedLLMStatus } from '@/api/embedded'
import { EmbeddedLLMInstallRecord } from './EmbeddedLLMInstallRecord'
import { makeEmbeddedStatus } from '@/test/embeddedStatusFixture'

function withEngine(engine: string): EmbeddedLLMStatus {
  return {
    ...makeEmbeddedStatus(),
    installed: true,
    engine,
    runtime_version: engine === 'mtplx' ? '2.12.2' : 'prism-b10735-842b188',
  }
}

let container: HTMLElement | null = null
let root: Root | null = null

async function render(status: EmbeddedLLMStatus): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<EmbeddedLLMInstallRecord status={status} />)
  })
}

function testId(id: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${id}"]`) ?? null
}

afterEach(() => {
  if (root) {
    act(() => root?.unmount())
  }
  container?.remove()
  container = null
  root = null
})

describe('the install record engine line', () => {
  it('renders MLX (MTPLX) for the mtplx engine', async () => {
    await render(withEngine('mtplx'))
    expect(testId('embedded-llm-engine')?.textContent).toBe('MLX (MTPLX)')
  })

  it('renders llama.cpp for the llama engine', async () => {
    await render(withEngine('llama'))
    expect(testId('embedded-llm-engine')?.textContent).toBe('llama.cpp')
  })

  it('renders llama.cpp for a pre-engine (empty) record', async () => {
    await render(withEngine(''))
    expect(testId('embedded-llm-engine')?.textContent).toBe('llama.cpp')
  })

  it('renders an unrecognized engine id as its raw id, never as a family guess', async () => {
    await render(withEngine('future-engine'))
    expect(testId('embedded-llm-engine')?.textContent).toBe('future-engine')
  })
})

describe('the mtplx informational records', () => {
  it('renders python_version and pack_revision beside the sibling fields', async () => {
    await render({
      ...withEngine('mtplx'),
      python_version: '3.12.15',
      pack_revision: '03bd60bb82755f2446f5083426dca9708eb8e0fe',
    })
    expect(testId('embedded-llm-python')?.textContent).toBe('3.12.15')
    expect(testId('embedded-llm-pack-revision')?.textContent).toBe('03bd60bb')
  })

  it('renders neither record when they are empty (the llama family)', async () => {
    await render(withEngine('llama'))
    expect(testId('embedded-llm-python')).toBeNull()
    expect(testId('embedded-llm-pack-revision')).toBeNull()
  })
})

describe('the engine-aware plan line', () => {
  const recordedPlan = {
    recorded: true,
    packing: '',
    kv_type: '',
    context_size: 94208,
    fit: false,
    fit_arg: '',
    fit_target_mib: 0,
    fit_min_context: 0,
    offload_mode: 'auto' as const,
    layers: -1,
    kv_offload: false,
    mmproj_offload: false,
    parallel: 0,
    cache_ram_mib: -1,
    devices: [],
    split_mode: '',
    gpu_family: '',
    device_budget_mib: 0,
    host_budget_mib: 0,
    expected_device_mib: 12288,
    expected_host_mib: 12288,
    notes: [],
  }

  it('renders the mtplx shape — window plus one unified pool, no llama fields', async () => {
    await render({ ...withEngine('mtplx'), plan: recordedPlan })
    const plan = testId('embedded-llm-plan')?.textContent ?? ''
    expect(plan).toContain('94208 ctx')
    expect(plan).toContain('12 GiB pool')
    expect(plan).not.toContain('fit')
    expect(plan).not.toContain('np ')
    expect(plan).not.toContain('device /')
  })

  it('keeps the llama vocabulary for the llama family', async () => {
    await render({ ...withEngine('llama'), plan: { ...recordedPlan, packing: 'PQ2_0', kv_type: 'q8_0' } })
    const plan = testId('embedded-llm-plan')?.textContent ?? ''
    expect(plan).toContain('PQ2_0')
    expect(plan).toContain('KV q8_0')
    expect(plan).toContain('fit off')
    expect(plan).toContain('np 0')
    expect(plan).toContain('12 GiB device / 12 GiB host')
  })

  it('renders an unrecorded plan as — under either engine', async () => {
    await render(withEngine('mtplx'))
    expect(testId('embedded-llm-plan')?.textContent).toBe('—')
  })
})

describe('the MTPLX attribution line', () => {
  it('renders on an mtplx install, naming MTPLX and linking the project', async () => {
    await render(withEngine('mtplx'))
    const line = testId('embedded-llm-attribution')
    expect(line?.textContent).toContain('Powered by MTPLX')
    const link = line?.querySelector('a')
    expect(link?.getAttribute('href')).toBe('https://github.com/youssofal/MTPLX')
  })

  it('does not render on a llama install', async () => {
    await render(withEngine('llama'))
    expect(testId('embedded-llm-attribution')).toBeNull()
  })

  it('does not render on a pre-engine record', async () => {
    await render(withEngine(''))
    expect(testId('embedded-llm-attribution')).toBeNull()
  })
})
