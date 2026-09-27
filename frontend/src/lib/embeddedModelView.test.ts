// @vitest-environment node
//
// lib/embeddedModelView — the pure state → surface derivation of the status-bar
// indicator, extracted from components/layout/EmbeddedModelStatus.tsx so it is
// testable without a renderer.
//
// This suite OWNS THE DERIVATION RULES, and components/layout/
// EmbeddedModelStatus.test.tsx (WIRING ONLY) owns the markup, the separator and
// the lifecycle. A change to a rule is made here, once — the component suite
// deliberately does not restate any of it.
//
// The contract pinned here: which surface the bar shows, the exact words of each
// tooltip, the per-artifact (never aggregated, never invented) progress fraction
// and the install ordering that picks it, and — above all — when there is
// NOTHING to say (null), because the component renders no separator in that case.

import { describe, it, expect } from 'vitest'
import {
  activeProgress,
  deriveView,
  loadedTitle,
  modelName,
} from './embeddedModelView'
import { makeEmbeddedStatus as makeStatus } from '@/test/embeddedStatusFixture'
import type { EmbeddedLLMProgressByComponent } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMInstallProgressData } from '@/types/events'

function progress(
  component: EmbeddedLLMInstallProgressData['component'],
  bytes_done: number,
  bytes_total: number,
  stage: EmbeddedLLMInstallProgressData['stage'] = 'downloading',
): EmbeddedLLMProgressByComponent {
  return { [component]: { component, stage, bytes_done, bytes_total } }
}

describe('activeProgress — the artifact whose transfer is in flight', () => {
  it('returns null before the first event and once everything is done', () => {
    expect(activeProgress({})).toBeNull()
    expect(
      activeProgress({
        ...progress('runtime', 1, 1, 'done'),
        ...progress('model', 2, 2, 'done'),
      }),
    ).toBeNull()
  })

  it('follows the install order, not the report order', () => {
    // The weights reported first, but the runtime is earlier in the order and
    // still in flight — it is the active artifact.
    const both = {
      ...progress('model', 100, 7_206_168_928),
      ...progress('runtime', 10, 100),
    }
    expect(activeProgress(both)?.component).toBe('runtime')
  })

  it('moves on once the earlier artifact is done', () => {
    const both = {
      ...progress('runtime', 100, 100, 'done'),
      ...progress('model', 100, 7_206_168_928),
    }
    expect(activeProgress(both)?.component).toBe('model')
  })
})

describe('deriveView — nothing to say', () => {
  it('is null before any snapshot arrives', () => {
    expect(deriveView(null, false, {})).toBeNull()
  })

  it('is null when the model is not installed', () => {
    expect(deriveView(makeStatus(), false, {})).toBeNull()
  })

  it('is null while the model is installed but stopped', () => {
    expect(
      deriveView(makeStatus({ state: 'installed', installed: true, packing: 'PQ2_0' }), false, {}),
    ).toBeNull()
  })

  it('ignores `available`: an unconstructable subsystem with nothing installed still has nothing to say', () => {
    // `available` is false only before startup (the agent directory is unset).
    // `deriveView` deliberately does NOT read it — the surface follows the
    // snapshot's own install/supervision fields — so this is null because
    // nothing is installed, not because the flag is down. The next case is what
    // would regress if the flag were ever folded into the contract.
    expect(deriveView(makeStatus({ available: false }), false, {})).toBeNull()
  })

  it('still paints an erroring snapshot while `available` is false', () => {
    const view = deriveView(
      makeStatus({ available: false, installed: true, state: 'error', error: 'launch failed' }),
      false,
      {},
    )
    expect(view?.kind).toBe('error')
    if (view?.kind !== 'error') throw new Error('expected the error surface')
    expect(view.message).toBe('launch failed')
  })
})

describe('deriveView — the install surface', () => {
  it('outranks every snapshot, including a loaded one', () => {
    const view = deriveView(makeStatus({ installed: true, loaded: true }), true, {})
    expect(view?.kind).toBe('install')
  })

  it('renders an indeterminate bar before the first event of a run', () => {
    const view = deriveView(null, true, {})
    expect(view).toEqual({
      kind: 'install',
      label: null,
      percent: null,
      title: 'Installing the embedded model…',
    })
  })

  it('reports the active artifact’s own percent and bytes — never an aggregate', () => {
    const view = deriveView(null, true, progress('model', 3_603_084_464, 7_206_168_928))
    expect(view?.kind).toBe('install')
    if (view?.kind !== 'install') throw new Error('expected the install surface')
    expect(view.label).toBe('Model weights')
    expect(view.percent).toBe(50)
    expect(view.title).toContain('Downloading')
    expect(view.title).toContain('3.4 GB / 6.7 GB')
  })

  it('clamps a percent above 100', () => {
    const view = deriveView(null, true, progress('runtime', 200, 100))
    if (view?.kind !== 'install') throw new Error('expected the install surface')
    expect(view.percent).toBe(100)
  })

  it('renders a byte-less stage without inventing a percentage', () => {
    const view = deriveView(null, true, progress('runtime', 0, 0, 'verifying'))
    if (view?.kind !== 'install') throw new Error('expected the install surface')
    expect(view.percent).toBeNull()
    expect(view.title).toContain('Verifying')
    expect(view.title).not.toContain('(')
  })
})

describe('deriveView — the load and residency surfaces', () => {
  it('renders an indeterminate bar while the weights load', () => {
    const view = deriveView(makeStatus({ state: 'loading', installed: true, loading: true }), false, {})
    expect(view?.kind).toBe('loading')
    expect(view?.title).toContain('takes minutes')
  })

  it('renders the resident model with its identity in the tooltip', () => {
    const view = deriveView(
      makeStatus({
        state: 'loaded',
        installed: true,
        loaded: true,
        packing: 'PQ2_0',
        backend: 'metal',
        context_size: 131072,
        base_url: 'http://127.0.0.1:43211/v1',
        pid: 4242,
        auto_unload_enabled: true,
        auto_unload_minutes: 45,
      }),
      false,
      {},
    )
    if (view?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(view.name).toBe('Bonsai 2 27B')
    expect(view.title).toContain('PQ2_0/metal')
    expect(view.title).toContain('context 131072')
    expect(view.title).toContain('serving on http://127.0.0.1:43211/v1')
    expect(view.title).toContain('pid 4242')
    expect(view.title).toContain('auto-unloads after 45 min idle')
  })

  it('states the residency policy when the idle timer is off', () => {
    const view = deriveView(makeStatus({ state: 'loaded', installed: true, loaded: true }), false, {})
    expect(view?.title).toContain('stays resident until unloaded')
  })

  it('never renders the idle countdown itself', () => {
    // `idle_remaining_seconds` only refreshes on a transition, so a number
    // derived from it would be wrong within a second — the tooltip states the
    // policy instead.
    const view = deriveView(
      makeStatus({
        state: 'loaded',
        installed: true,
        loaded: true,
        auto_unload_enabled: true,
        idle_remaining_seconds: 1234,
      }),
      false,
      {},
    )
    expect(view?.title).not.toContain('1234')
  })
})

describe('deriveView — the error surface', () => {
  it('carries the supervisor’s message and points at the Settings block', () => {
    const view = deriveView(
      makeStatus({ state: 'error', installed: true, error: 'the runtime exited (1)' }),
      false,
      {},
    )
    if (view?.kind !== 'error') throw new Error('expected the error surface')
    expect(view.message).toBe('the runtime exited (1)')
    expect(view.title).toContain('Settings → LLM → Embedded LLM')
  })

  it('surfaces a failure message even while nothing is installed', () => {
    const view = deriveView(makeStatus({ error: 'the install failed' }), false, {})
    expect(view?.kind).toBe('error')
  })

  it('says something when the state is error but the message is empty', () => {
    const view = deriveView(makeStatus({ state: 'error', installed: true }), false, {})
    if (view?.kind !== 'error') throw new Error('expected the error surface')
    expect(view.message).toBe('The embedded model failed.')
  })

  it('gives way to a live install run', () => {
    const view = deriveView(
      makeStatus({ state: 'error', installed: true, error: 'the install failed' }),
      true,
      progress('runtime', 1, 2),
    )
    expect(view?.kind).toBe('install')
  })
})

describe('modelName / loadedTitle — the identity fallbacks', () => {
  it('prefers the model name, then the bare composite id, then a generic label', () => {
    expect(modelName(makeStatus({ model_name: 'Bonsai 2 27B' }))).toBe('Bonsai 2 27B')
    expect(modelName(makeStatus({ model_name: '', model_id: 'embedded/Bonsai 2 27B' }))).toBe(
      'Bonsai 2 27B',
    )
    expect(modelName(makeStatus({ model_name: '', model_id: '' }))).toBe('Embedded model')
  })

  it('omits the identity part when neither packing nor backend was recorded', () => {
    const title = loadedTitle(makeStatus({ model_name: 'Bonsai 2 27B' }), 'Bonsai 2 27B')
    expect(title).toBe('Embedded model resident — Bonsai 2 27B · stays resident until unloaded')
  })

  it('omits the zero-valued parts rather than printing zeros', () => {
    const title = loadedTitle(
      makeStatus({ packing: 'PTQ1_0', context_size: 0, base_url: '', pid: 0 }),
      'Bonsai 2 27B',
    )
    expect(title).toContain('PTQ1_0')
    expect(title).not.toContain('context')
    expect(title).not.toContain('serving on')
    expect(title).not.toContain('pid')
  })
})
