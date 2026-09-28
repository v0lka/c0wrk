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
  isEmbeddedSessionModel,
  loadedTitle,
  modelName,
} from './embeddedModelView'
import { makeEmbeddedStatus as makeStatus } from '@/test/embeddedStatusFixture'
import type { EmbeddedLLMProgressByComponent } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMInstallProgressData } from '@/types/events'
import type { TokenInfo } from '@/types/models'

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

describe('deriveView / sessionThroughput — the residency throughput metric', () => {
  // The resident snapshot every case below starts from: the realistic install
  // (model_name "Bonsai 2 27B", composite model_id "embedded/Bonsai 2 27B").
  const resident = makeStatus({ state: 'loaded', installed: true, loaded: true })

  function tokens(overrides: Partial<TokenInfo> = {}): TokenInfo {
    return {
      total_input_tokens: 12_000,
      total_output_tokens: 3_400,
      model: 'Bonsai 2 27B',
      family: 'embedded',
      median_output_tok_s: 27.44,
      tok_s_samples: 5,
      ...overrides,
    }
  }

  it('appends the median rate to the loaded surface when the session runs this model', () => {
    const view = deriveView(resident, false, {}, tokens())
    if (view?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(view.tokPerSec).toBe('27.4')
    expect(view.tokSamples).toBe(5)
    expect(view.name).toBe('Bonsai 2 27B')
    expect(view.title).toContain('end-to-end per request · median of last 5 samples')
  })

  it('accepts the composite "embedded/…" selector via bareModel', () => {
    // The backend's canonical selector form — the provider prefix must not
    // fail the gate.
    const view = deriveView(
      resident,
      false,
      {},
      tokens({ model: 'embedded/Bonsai 2 27B' }),
    )
    if (view?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(view.tokPerSec).toBe('27.4')
  })

  it('gates on the model: a session on another model shows no rate', () => {
    const view = deriveView(resident, false, {}, tokens({ model: 'qwen3.6-35b-a3b' }))
    if (view?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(view.tokPerSec).toBeNull()
    expect(view.tokSamples).toBe(0)
    // …and the tooltip gains nothing either.
    expect(view.title).not.toContain('median of last')
  })

  it('gates on the sample count: fewer than three samples show no rate', () => {
    const two = deriveView(resident, false, {}, tokens({ tok_s_samples: 2 }))
    if (two?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(two.tokPerSec).toBeNull()
    expect(two.title).not.toContain('median of last')

    // Exactly three — the documented floor — shows.
    const three = deriveView(resident, false, {}, tokens({ tok_s_samples: 3 }))
    if (three?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(three.tokPerSec).toBe('27.4')
    expect(three.tokSamples).toBe(3)
  })

  it('renders exactly the pre-metric surface when the tokens carry no metric', () => {
    const without = deriveView(
      resident,
      false,
      {},
      tokens({ median_output_tok_s: undefined, tok_s_samples: undefined }),
    )
    const baseline = deriveView(resident, false, {})
    if (without?.kind !== 'loaded' || baseline?.kind !== 'loaded') {
      throw new Error('expected the loaded surface')
    }
    expect(without.tokPerSec).toBeNull()
    expect(without.tokSamples).toBe(0)
    // The pre-metric title is preserved byte for byte.
    expect(without.title).toBe(baseline.title)
  })

  it('treats a non-positive median as no metric', () => {
    const view = deriveView(resident, false, {}, tokens({ median_output_tok_s: 0 }))
    if (view?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(view.tokPerSec).toBeNull()
  })

  it('drops trailing ".0" and keeps one decimal otherwise', () => {
    const integral = deriveView(resident, false, {}, tokens({ median_output_tok_s: 42 }))
    if (integral?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(integral.tokPerSec).toBe('42')

    const fractional = deriveView(resident, false, {}, tokens({ median_output_tok_s: 13.76 }))
    if (fractional?.kind !== 'loaded') throw new Error('expected the loaded surface')
    expect(fractional.tokPerSec).toBe('13.8')
  })

  it('never lets the metric alone create a surface (null contract intact)', () => {
    expect(deriveView(null, false, {}, tokens())).toBeNull()
    expect(deriveView(makeStatus(), false, {}, tokens())).toBeNull()
  })
})

describe('isEmbeddedSessionModel — the model-name gate', () => {
  const resident = makeStatus({ state: 'loaded', installed: true, loaded: true })

  it('matches the bare display name and the composite selector form', () => {
    expect(isEmbeddedSessionModel({ model: 'Bonsai 2 27B' }, resident)).toBe(true)
    expect(isEmbeddedSessionModel({ model: 'embedded/Bonsai 2 27B' }, resident)).toBe(true)
  })

  it('falls back to bareModel(model_id) when the snapshot has no model_name', () => {
    const unnamed = makeStatus({ model_name: '', model_id: 'embedded/Bonsai 2 27B' })
    expect(isEmbeddedSessionModel({ model: 'Bonsai 2 27B' }, unnamed)).toBe(true)
  })

  it('compares normalized: case and surrounding whitespace do not matter', () => {
    expect(isEmbeddedSessionModel({ model: '  bonsai 2 27b ' }, resident)).toBe(true)
    expect(
      isEmbeddedSessionModel({ model: 'embedded/Bonsai 2 27B' }, makeStatus({ model_name: '  BONSAI 2 27B' })),
    ).toBe(true)
  })

  it('rejects other models and empty input', () => {
    expect(isEmbeddedSessionModel({ model: 'gpt-5.2' }, resident)).toBe(false)
    expect(isEmbeddedSessionModel({ model: 'bonsai 2 27b mini' }, resident)).toBe(false)
    expect(isEmbeddedSessionModel({ model: '' }, resident)).toBe(false)
    expect(isEmbeddedSessionModel(null, resident)).toBe(false)
    expect(isEmbeddedSessionModel(undefined, resident)).toBe(false)
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

  it('never paints an install failure — install_error is a Settings-only surface', () => {
    // A failed install used to leak into status.error and paint the bar with
    // raw transport diagnostics. The backend contract now splits the fields:
    // a fatal install failure arrives as install_error (rendered by the
    // Settings block alone), and a resumable one never arrives at all — the
    // backend retries it silently. Either way the bar has nothing to say.
    const view = deriveView(
      makeStatus({ install_error: 'the download server could not be reached after 3 attempts' }),
      false,
      {},
    )
    expect(view).toBeNull()
  })

  it('still paints the supervisor error message while nothing is installed', () => {
    const view = deriveView(makeStatus({ state: 'error', error: 'the runtime exited (1)' }), false, {})
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
