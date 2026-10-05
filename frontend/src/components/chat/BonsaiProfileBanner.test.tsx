// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ModelInfo } from '@/types/models'

const config = vi.hoisted(() => ({ defaultModel: 'embedded/Bonsai 2 27B', allModels: [] as ModelInfo[] }))
vi.mock('@/hooks/useConfigData', () => ({ useConfigData: () => ({ ...config, loaded: true }) }))

import { BonsaiProfileBanner } from './BonsaiProfileBanner'
import { useModelProfilesGateStore } from '@/stores/modelProfilesGateStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useSettingsStore } from '@/stores/settingsStore'

let root: Root
let container: HTMLDivElement

beforeEach(() => {
  config.defaultModel = 'embedded/Bonsai 2 27B'
  config.allModels = [
    { provider: 'embedded', name: 'Bonsai 2 27B', family: 'qwen3', vision: true },
    { provider: 'remote', name: 'Bonsai 2 27B', family: 'qwen3', vision: true },
    { provider: 'remote', name: 'other', family: 'qwen3', vision: false },
  ]
  useModelProfilesGateStore.setState({
    enabled: true, essentialToolsEnabled: false, loaded: true,
    activeProfileId: 'generic', suggestedProfileId: 'bonsai-2-27b',
  })
  useInputModeStore.setState({ selectedModel: null })
  useSettingsStore.setState({ open: false, activeTab: 'general' })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function render() {
  act(() => root.render(<BonsaiProfileBanner />))
  return container.querySelector('button')
}

describe('BonsaiProfileBanner', () => {
  it('shows a one-line warning above chat for the embedded default with a different active profile', () => {
    const banner = render()
    expect(banner?.textContent).toContain('Bonsai profile')
    expect(banner?.className).toContain('text-warning')
    expect(banner?.className).toContain('bg-warning/10')
    expect(banner?.querySelector('span')?.className).toContain('truncate')
  })

  it('opens Settings → Model Profiles on click without applying a profile', () => {
    const banner = render()
    act(() => banner?.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    expect(useSettingsStore.getState()).toMatchObject({ open: true, activeTab: 'model-profiles' })
    expect(useModelProfilesGateStore.getState()).toMatchObject({ enabled: true, activeProfileId: 'generic' })
  })

  it('is hidden when profiles are disabled even with a Bonsai suggestion and model', () => {
    useModelProfilesGateStore.setState({ enabled: false })
    expect(render()).toBeNull()
  })

  it('is hidden when the Bonsai profile is already active', () => {
    useModelProfilesGateStore.setState({ activeProfileId: 'bonsai-2-27b' })
    expect(render()).toBeNull()
  })

  it('is hidden before profile identity has loaded', () => {
    useModelProfilesGateStore.setState({ activeProfileId: null, loaded: false })
    expect(render()).toBeNull()
  })

  it.each(['remote/other', 'remote/Bonsai 2 27B', 'unknown/model', ''])('is hidden for override %j even with an embedded default', (selectedModel) => {
    useInputModeStore.setState({ selectedModel })
    expect(render()).toBeNull()
  })

  it('shows for an embedded override despite a remote default and unrelated suggestion', () => {
    config.defaultModel = 'remote/other'
    useInputModeStore.setState({ selectedModel: 'embedded/Bonsai 2 27B' })
    useModelProfilesGateStore.setState({ suggestedProfileId: 'qwen3.8-27b' })
    expect(render()).not.toBeNull()
  })

  it('resolves the bare default name through the model catalog', () => {
    config.defaultModel = 'Bonsai 2 27B'
    expect(render()).not.toBeNull()
  })

  it('does not mistake another provider with the same bare model name for embedded Bonsai', () => {
    config.defaultModel = 'Bonsai 2 27B'
    config.allModels.reverse()
    expect(render()).toBeNull()
  })

  it('hides an unresolved model when the embedded entry is absent', () => {
    config.allModels = []
    expect(render()).toBeNull()
  })

  it('reacts immediately to profile, master toggle, and selected-model changes', () => {
    expect(render()).not.toBeNull()
    act(() => useModelProfilesGateStore.getState().setProfileIds('bonsai-2-27b', 'bonsai-2-27b'))
    expect(container.querySelector('button')).toBeNull()
    act(() => useModelProfilesGateStore.getState().setProfileIds('custom', null))
    expect(container.querySelector('button')).not.toBeNull()
    act(() => useModelProfilesGateStore.getState().setEnabled(false))
    expect(container.querySelector('button')).toBeNull()
    act(() => useModelProfilesGateStore.getState().setEnabled(true))
    expect(container.querySelector('button')).not.toBeNull()
    act(() => useInputModeStore.getState().setSelectedModel('remote/other'))
    expect(container.querySelector('button')).toBeNull()
  })
})
