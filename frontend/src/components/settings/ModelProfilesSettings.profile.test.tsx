// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mock the API layer: the component must not touch real Wails bindings.
// updateExperimentalFeatures toggles the experimental-features switch, whose
// only gated feature is the E2S execution mode — Model Profiles graduated out
// of that gate (ADR-044), so the profile UI must never call it (UX invariant:
// neither switching a profile nor the master profile toggle touches the gate).
const getModelProfilesMock = vi.fn()
const updateModelProfileMock = vi.fn()
const createModelProfileMock = vi.fn()
const deleteModelProfileMock = vi.fn()
const selectModelProfileMock = vi.fn()
const setModelProfilesEnabledMock = vi.fn()
const updateExperimentalFeaturesMock = vi.fn()
vi.mock('@/api/config', () => ({
  getModelProfiles: (...args: unknown[]) => getModelProfilesMock(...args),
  updateModelProfile: (...args: unknown[]) => updateModelProfileMock(...args),
  createModelProfile: (...args: unknown[]) => createModelProfileMock(...args),
  deleteModelProfile: (...args: unknown[]) => deleteModelProfileMock(...args),
  selectModelProfile: (...args: unknown[]) => selectModelProfileMock(...args),
  setModelProfilesEnabled: (...args: unknown[]) => setModelProfilesEnabledMock(...args),
  updateExperimentalFeatures: (...args: unknown[]) => updateExperimentalFeaturesMock(...args),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide — the always-present combobox
// and the profile selector portal their menus into document.body.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

import { TooltipProvider } from '@/components/ui/tooltip'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { focusIn, focusOut, pressKey, typeValue } from '@/test/domNumberInput'
import type { ModelProfilesResponse, ModelProfileValues } from '@/types/models'
import { ModelProfilesSettings } from './ModelProfilesSettings'

const builtinTools = [
  { name: 'ask_user', description: 'ask the user a question' },
  { name: 'bash_exec', description: 'run shell commands.' },
  { name: 'finish', description: 'finish the task' },
  { name: 'read_file', description: 'read a file' },
]
const toolGroups = [
  {
    id: 'subagents',
    title: 'Subagents & reflection',
    description: 'Delegate work to subagents.',
    tools: ['bash_exec'],
  },
]
const protectedTools = ['ask_user', 'finish']

const baseValues: ModelProfileValues = {
  essential_tools: {
    enabled: true,
    always_present: ['finish', 'read_file'],
    compact_descriptions: false,
  },
  system_prompt: { lite: false, few_shot: false, reasoning_scaffold: false },
  sampling: {
    enabled: true,
    temperature: 0,
    top_p: 0,
    top_k: 0,
    repetition_penalty: 0,
    presence_penalty: 0,
    reasoning_effort: 'medium',
  },
  loop_hardening: {
    enabled: true,
    repeat_nudge_threshold: 2,
    parse_error_abort_threshold: 3,
    fruitless_nudge_threshold: 3,
    fruitless_abort_threshold: 4,
    same_tool_repeat_nudge_threshold: 5,
  },
  context: {
    enabled: true,
    compaction: { keep_last: 6, block_size: 5, trigger_percent: 80 },
    tool_output_keep_last_n: 2,
    output_token_reserve: 16384,
  },
}

const genericProfile = { id: 'generic', name: 'Generic (model-agnostic)', kind: 'predefined' as const, values: structuredClone(baseValues) }
const qwenProfile = { id: 'qwen3.8-27b', name: 'Qwen3.8-27B (minimal)', kind: 'predefined' as const, values: structuredClone(baseValues) }
const gemmaProfile = { id: 'gemma-4-26b-a4b-it', name: 'Gemma-4-26B-A4B-it (maximal)', kind: 'predefined' as const, values: structuredClone(baseValues) }
const customProfile = { id: 'test-tuned', name: 'Test Tuned', kind: 'custom' as const, values: structuredClone(baseValues) }

/** Response fixture with per-field overrides. */
function resp(overrides: {
  enabled?: boolean
  active_id?: string
  suggested?: string | null
  profiles?: ModelProfilesResponse['profiles']
  warnings?: string[]
}): ModelProfilesResponse {
  return structuredClone({
    enabled: overrides.enabled ?? true,
    profiles: overrides.profiles ?? [genericProfile, qwenProfile, gemmaProfile, customProfile],
    active_id: overrides.active_id ?? 'test-tuned',
    suggested_profile_id: overrides.suggested ?? null,
    builtin_tools: builtinTools,
    tool_groups: toolGroups,
    protected_tools: protectedTools,
    warnings: overrides.warnings ?? [],
  })
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  getModelProfilesMock.mockReset().mockResolvedValue(resp({}))
  updateModelProfileMock.mockReset().mockResolvedValue(undefined)
  createModelProfileMock.mockReset().mockResolvedValue('my-copy')
  deleteModelProfileMock.mockReset().mockResolvedValue(undefined)
  selectModelProfileMock.mockReset().mockResolvedValue(undefined)
  setModelProfilesEnabledMock.mockReset().mockResolvedValue(undefined)
  updateExperimentalFeaturesMock.mockClear()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  document.body.innerHTML = ''
})

const render = () =>
  act(async () => {
    await root.render(
      <TooltipProvider>
        <ModelProfilesSettings />
      </TooltipProvider>,
    )
  })

const field = (label: string) =>
  container.querySelector(`input[data-field="${label}"]`) as HTMLInputElement | null

/** The checkbox of the Toggle row containing `labelText`. */
const toggleFor = (labelText: string) => {
  const span = Array.from(container.querySelectorAll('span')).find((s) => s.textContent === labelText)
  return span?.closest('div')?.querySelector('input[type="checkbox"]') as HTMLInputElement | null
}

const buttonByLabel = (label: string) =>
  container.querySelector(`button[aria-label="${label}"]`) as HTMLButtonElement | null

const selectorTrigger = () => buttonByLabel('Select Model Profiles profile')

/** Open the profile selector's Radix menu (toggles on pointerdown). */
async function openSelector(): Promise<void> {
  const btn = selectorTrigger()
  expect(btn).not.toBeNull()
  await act(async () => {
    btn!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
}

const menuItems = () => Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))
const menuItem = (profileId: string) => {
  const el = menuItems().find((m) => m.dataset.profileId === profileId)
  if (!el) throw new Error(`Menu item for "${profileId}" not found`)
  return el
}
const menuLabels = () =>
  Array.from(document.body.querySelectorAll('[role="menu"] > *')).map((el) => el.textContent ?? '')

const dialogEl = (kind: string) =>
  document.body.querySelector(`[data-testid="model_profiles-profile-dialog-${kind}"]`) as HTMLElement | null

const nameInput = () =>
  document.body.querySelector<HTMLInputElement>('[data-testid="model_profiles-profile-name-input"]')

const setName = (input: HTMLInputElement, value: string) =>
  act(async () => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
    setter?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })

const dialogButton = (label: string) =>
  Array.from(document.body.querySelectorAll<HTMLButtonElement>('button')).find((b) =>
    b.getAttribute('aria-label') === label || b.textContent === label,
  )

const suggestionBanner = () =>
  container.querySelector('[data-testid="model_profiles-suggestion-banner"]') ??
  document.body.querySelector('[data-testid="model_profiles-suggestion-banner"]')

describe('ModelProfilesSettings — profile selector', () => {
  it('renders grouped options: Predefined profiles, then Custom, with the active one marked', async () => {
    await render()
    // The trigger shows the active profile name.
    expect(selectorTrigger()?.textContent).toContain('Test Tuned')

    await openSelector()
    const labels = menuLabels()
    expect(labels).toContain('Predefined')
    expect(labels).toContain('Custom')
    // Group order: predefined items before the Custom label.
    expect(labels.indexOf('Predefined')).toBeLessThan(labels.indexOf('Custom'))
    const ids = menuItems().map((m) => m.dataset.profileId)
    expect(ids).toEqual(['generic', 'qwen3.8-27b', 'gemma-4-26b-a4b-it', 'test-tuned'])
    // The active profile is marked selected.
    expect(menuItem('test-tuned').dataset.selected).toBe('true')
    expect(menuItem('generic').dataset.selected).toBe('false')
  })

  it('selecting another profile calls selectModelProfile, reloads and updates the trigger', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned' }))
      .mockResolvedValueOnce(resp({ active_id: 'generic' }))
    await render()
    await openSelector()
    await act(async () => {
      menuItem('generic').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(selectModelProfileMock).toHaveBeenCalledWith('generic')
    expect(getModelProfilesMock).toHaveBeenCalledTimes(2)
    expect(selectorTrigger()?.textContent).toContain('Generic (model-agnostic)')
  })

  it('re-picking the active profile is a no-op (no RPC)', async () => {
    await render()
    await openSelector()
    await act(async () => {
      menuItem('test-tuned').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(selectModelProfileMock).not.toHaveBeenCalled()
  })

  it('UX invariant: switching a profile never touches the experimental (E2S) switch', async () => {
    const before = useExperimentalStore.getState().enabled
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned' }))
      .mockResolvedValueOnce(resp({ active_id: 'qwen3.8-27b' }))
    await render()
    await openSelector()
    await act(async () => {
      menuItem('qwen3.8-27b').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(selectModelProfileMock).toHaveBeenCalledTimes(1)
    expect(updateExperimentalFeaturesMock).not.toHaveBeenCalled()
    expect(useExperimentalStore.getState().enabled).toBe(before)
  })
})

describe('ModelProfilesSettings — predefined active profile (read-only view)', () => {
  beforeEach(() => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'generic' }))
  })

  it('renders the sections with every control disabled', async () => {
    await render()
    expect(toggleFor('Curate the tool subset')?.disabled).toBe(true)
    expect(toggleFor('Compact tool descriptions')?.disabled).toBe(true)
    expect(field('Temperature')?.disabled).toBe(true)
    expect(field('Keep last')?.disabled).toBe(true)
    expect(buttonByLabel('Add Always-present tools')?.disabled).toBe(true)
    // The profile selector itself stays enabled — switching away is allowed.
    expect(selectorTrigger()?.disabled).toBe(false)
  })

  it('disabled controls never emit an update', async () => {
    await render()
    await act(async () => {
      toggleFor('Curate the tool subset')?.click()
    })
    expect(updateModelProfileMock).not.toHaveBeenCalled()
  })

  it('offers Duplicate (no Rename/Delete) and creates + activates an editable copy', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'generic' }))
      .mockResolvedValueOnce(
        resp({
          active_id: 'my-copy',
          profiles: [
            genericProfile,
            qwenProfile,
            gemmaProfile,
            customProfile,
            { id: 'my-copy', name: 'My Copy', kind: 'custom', values: structuredClone(baseValues) },
          ],
        }),
      )
    await render()
    expect(buttonByLabel('Rename profile')).toBeNull()
    expect(buttonByLabel('Delete profile')).toBeNull()

    await act(async () => {
      buttonByLabel('Duplicate profile')?.click()
    })
    expect(dialogEl('duplicate')).not.toBeNull()
    // The name input is prefilled with "<name> (copy)".
    expect(nameInput()?.value).toBe('Generic (model-agnostic) (copy)')

    await setName(nameInput()!, 'My Copy')
    await act(async () => {
      dialogButton('Confirm duplicate profile')?.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(createModelProfileMock).toHaveBeenCalledWith('generic', 'My Copy')
    expect(selectModelProfileMock).toHaveBeenCalledWith('my-copy')
    // The dialog closed and the copy is now the active, editable profile.
    expect(dialogEl('duplicate')).toBeNull()
    expect(selectorTrigger()?.textContent).toContain('My Copy')
    expect(toggleFor('Curate the tool subset')?.disabled).toBe(false)
  })

  it('an empty name cannot be submitted', async () => {
    await render()
    await act(async () => {
      buttonByLabel('Duplicate profile')?.click()
    })
    await setName(nameInput()!, '   ')
    // The confirm button is disabled for a blank name.
    expect(dialogButton('Confirm duplicate profile')?.disabled).toBe(true)
    expect(createModelProfileMock).not.toHaveBeenCalled()
  })
})

describe('ModelProfilesSettings — custom profile CRUD', () => {
  beforeEach(() => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'test-tuned' }))
  })

  it('shows Rename and Delete (no Duplicate) and saves a rename through the dialog', async () => {
    await render()
    expect(buttonByLabel('Duplicate profile')).toBeNull()

    await act(async () => {
      buttonByLabel('Rename profile')?.click()
    })
    expect(dialogEl('rename')).not.toBeNull()
    expect(nameInput()?.value).toBe('Test Tuned')

    await setName(nameInput()!, 'Renamed')
    await act(async () => {
      dialogButton('Confirm rename profile')?.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(updateModelProfileMock).toHaveBeenCalledWith('test-tuned', { name: 'Renamed' })
    expect(dialogEl('rename')).toBeNull()
  })

  it('delete requires an explicit confirmation and reloads after', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned' }))
      .mockResolvedValueOnce(resp({ active_id: 'generic', warnings: ['the active profile was deleted; switched to generic'] }))
    await render()
    await act(async () => {
      buttonByLabel('Delete profile')?.click()
    })
    const dlg = dialogEl('delete')
    expect(dlg).not.toBeNull()
    expect(dlg?.textContent).toContain('Test Tuned')
    // No RPC before the confirmation button is pressed.
    expect(deleteModelProfileMock).not.toHaveBeenCalled()

    await act(async () => {
      dialogButton('Confirm delete profile')?.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(deleteModelProfileMock).toHaveBeenCalledWith('test-tuned')
    expect(dialogEl('delete')).toBeNull()
    // The reload picked up the generic fallback + its one-shot notice.
    expect(selectorTrigger()?.textContent).toContain('Generic (model-agnostic)')
    expect(container.textContent).toContain('switched to generic')
  })

  it('cancelling the delete dialog sends no RPC', async () => {
    await render()
    await act(async () => {
      buttonByLabel('Delete profile')?.click()
    })
    await act(async () => {
      dialogButton('Cancel')?.click()
    })
    expect(dialogEl('delete')).toBeNull()
    expect(deleteModelProfileMock).not.toHaveBeenCalled()
  })
})

describe('ModelProfilesSettings — suggestion banner', () => {
  it('is hidden when no profile is suggested', async () => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'test-tuned', suggested: null }))
    await render()
    expect(suggestionBanner()).toBeNull()
  })

  it('is hidden when the suggested profile is already active', async () => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'qwen3.8-27b', suggested: 'qwen3.8-27b' }))
    await render()
    expect(suggestionBanner()).toBeNull()
  })

  it('appears when suggested differs from active; Apply selects it and the banner disappears', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned', suggested: 'qwen3.8-27b' }))
      .mockResolvedValueOnce(resp({ active_id: 'qwen3.8-27b', suggested: 'qwen3.8-27b' }))
    await render()
    const banner = suggestionBanner()
    expect(banner).not.toBeNull()
    expect(banner?.textContent).toContain('Qwen3.8-27B (minimal)')

    await act(async () => {
      buttonByLabel('Apply suggested profile')?.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    // Apply is an explicit select — never an auto-apply on load.
    expect(selectModelProfileMock).toHaveBeenCalledTimes(1)
    expect(selectModelProfileMock).toHaveBeenCalledWith('qwen3.8-27b')
    expect(suggestionBanner()).toBeNull()
  })

  it('Hide dismisses without selecting; the same suggestion stays hidden', async () => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'test-tuned', suggested: 'qwen3.8-27b' }))
    await render()
    expect(suggestionBanner()).not.toBeNull()
    await act(async () => {
      buttonByLabel('Dismiss suggested profile')?.click()
    })
    expect(suggestionBanner()).toBeNull()
    expect(selectModelProfileMock).not.toHaveBeenCalled()
    // A reload (any profile op) keeps the same suggestion dismissed.
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'generic', suggested: 'qwen3.8-27b' }))
    await openSelector()
    await act(async () => {
      menuItem('generic').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(suggestionBanner()).toBeNull()
  })

  it('a different suggestion (model switch) re-opens the banner', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned', suggested: 'qwen3.8-27b' }))
      .mockResolvedValueOnce(resp({ active_id: 'test-tuned', suggested: 'gemma-4-26b-a4b-it' }))
    await render()
    await act(async () => {
      buttonByLabel('Dismiss suggested profile')?.click()
    })
    expect(suggestionBanner()).toBeNull()
    // Switching the default model changes the suggestion — banner returns.
    await openSelector()
    await act(async () => {
      menuItem('generic').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })
    const banner = suggestionBanner()
    expect(banner).not.toBeNull()
    expect(banner?.textContent).toContain('Gemma-4-26B-A4B-it (maximal)')
  })
})

describe('ModelProfilesSettings — dangling active profile fallback', () => {
  it('warns and renders the generic profile read-only', async () => {
    getModelProfilesMock.mockResolvedValue(resp({ active_id: 'ghost-profile' }))
    await render()
    const warn = container.querySelector('[data-testid="model_profiles-fallback-warning"]')
    expect(warn).not.toBeNull()
    expect(warn?.textContent).toContain('ghost-profile')
    expect(warn?.textContent).toContain('Generic (model-agnostic)')
    // The effective (generic) values render read-only; the selector flags the unknown id.
    expect(toggleFor('Curate the tool subset')?.disabled).toBe(true)
    expect(selectorTrigger()?.textContent).toContain('Unknown profile (ghost-profile)')
    expect(buttonByLabel('Duplicate profile')).not.toBeNull()
  })

  it('suppresses the resolver warning that merely restates the fallback banner', async () => {
    getModelProfilesMock.mockResolvedValue(
      resp({
        active_id: 'ghost-profile',
        warnings: [
          'model_profiles.active_profile "ghost-profile" not found in the profile catalog; falling back to the "generic" profile',
        ],
      }),
    )
    await render()
    expect(container.querySelector('[data-testid="model_profiles-fallback-warning"]')).not.toBeNull()
    expect(container.textContent ?? '').not.toContain('not found in the profile catalog')
  })

  it('still renders unrelated warnings alongside the fallback banner', async () => {
    getModelProfilesMock.mockResolvedValue(
      resp({
        active_id: 'ghost-profile',
        warnings: ['model_profiles profiles: entry 1 skipped: boom'],
      }),
    )
    await render()
    expect(container.textContent ?? '').toContain('entry 1 skipped: boom')
  })
})

describe('ModelProfilesSettings — always-present chips render protected tools', () => {
  // Fixture: always_present = ['finish', 'read_file']; protected = ['ask_user',
  // 'finish']. `ask_user` is protected but NOT pinned, so it is exactly the
  // case the display-only union must surface as a locked chip.
  it('renders a protected tool absent from always_present as a locked chip', async () => {
    await render()
    const chips = Array.from(container.querySelectorAll('code.font-mono')).map((c) => c.textContent)
    expect(chips).toContain('finish')
    expect(chips).toContain('read_file')
    expect(chips).toContain('ask_user')
    // Locked chips (protected) expose no remove button; a plain pin still does.
    expect(container.querySelector('button[aria-label="Remove ask_user"]')).toBeNull()
    expect(container.querySelector('button[aria-label="Remove finish"]')).toBeNull()
    expect(container.querySelector('button[aria-label="Remove read_file"]')).not.toBeNull()
  })

  it('never persists the display-only protected tools into always_present', async () => {
    await render()
    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[aria-label="Remove read_file"]')!.click()
    })
    expect(updateModelProfileMock).toHaveBeenCalledTimes(1)
    const payload = updateModelProfileMock.mock.calls[0]![1] as {
      config: { essential_tools: { always_present: string[] } }
    }
    // `finish` was pinned; `ask_user` was only unioned for display — neither the
    // removed pin nor the unioned protected tool may leak into the saved value.
    expect(payload.config.essential_tools.always_present).toEqual(['finish'])
  })
})

describe('ModelProfilesSettings — master toggle (model_profiles.enabled)', () => {
  const masterToggle = () => toggleFor('Enable the Model Profiles profile')

  const clickMasterToggle = () =>
    act(async () => {
      masterToggle()?.click()
      await new Promise((r) => setTimeout(r, 0))
    })

  it('hides every profile control while disabled, showing only the toggle and a hint', async () => {
    getModelProfilesMock.mockResolvedValue(resp({ enabled: false }))
    await render()

    // The master toggle is present and unchecked.
    const toggle = masterToggle()
    expect(toggle).not.toBeNull()
    expect(toggle?.checked).toBe(false)

    // No profile picker, no CRUD actions, no variant sections.
    expect(selectorTrigger()).toBeNull()
    expect(buttonByLabel('Duplicate profile')).toBeNull()
    expect(buttonByLabel('Rename profile')).toBeNull()
    expect(toggleFor('Curate the tool subset')).toBeFalsy()
    expect(field('Temperature')).toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('The Model Profiles profile is off')
    expect(text).not.toContain('Context Management')
  })

  it('turning it on calls setModelProfilesEnabled(true) and reveals the profile UI', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ enabled: false }))
      .mockResolvedValueOnce(resp({ enabled: true }))
    await render()
    expect(selectorTrigger()).toBeNull()

    await clickMasterToggle()

    expect(setModelProfilesEnabledMock).toHaveBeenCalledWith(true)
    // The catalog was reloaded after the toggle RPC.
    expect(getModelProfilesMock).toHaveBeenCalledTimes(2)
    // The reloaded (enabled) response renders the full profile UI.
    expect(selectorTrigger()).not.toBeNull()
    expect(toggleFor('Curate the tool subset')).not.toBeNull()
    expect(container.textContent).toContain('Context Management')
    expect(masterToggle()?.checked).toBe(true)
  })

  it('turning it off hides the profile UI again', async () => {
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ enabled: true }))
      .mockResolvedValueOnce(resp({ enabled: false }))
    await render()
    expect(selectorTrigger()).not.toBeNull()

    await clickMasterToggle()

    expect(setModelProfilesEnabledMock).toHaveBeenCalledWith(false)
    expect(getModelProfilesMock).toHaveBeenCalledTimes(2)
    expect(selectorTrigger()).toBeNull()
    expect(container.textContent).not.toContain('Context Management')
    expect(masterToggle()?.checked).toBe(false)
  })

  it('UX invariant: the master profile toggle never touches the experimental (E2S) switch', async () => {
    const before = useExperimentalStore.getState().enabled
    getModelProfilesMock
      .mockResolvedValueOnce(resp({ enabled: false }))
      .mockResolvedValueOnce(resp({ enabled: true }))
    await render()

    await clickMasterToggle()

    expect(setModelProfilesEnabledMock).toHaveBeenCalledWith(true)
    expect(updateExperimentalFeaturesMock).not.toHaveBeenCalled()
    expect(useExperimentalStore.getState().enabled).toBe(before)
  })
})

describe('ModelProfilesSettings — independent of the experimental switch', () => {
  // ADR-044: Model Profiles graduated out of the experimental gate. The switch
  // being off (the state that used to hide the profile surface) must not gate
  // the profile UI — it renders fully interactive while the gate is off.
  it('renders the full profile UI while the experimental switch is off', async () => {
    useExperimentalStore.setState({ enabled: false, loaded: true })
    await render()

    expect(selectorTrigger()).not.toBeNull()
    expect(selectorTrigger()?.disabled).toBe(false)
    expect(toggleFor('Curate the tool subset')).not.toBeNull()
    expect(container.textContent).toContain('Context Management')

    // Rendering the profile surface leaves the experimental switch untouched.
    expect(useExperimentalStore.getState().enabled).toBe(false)
  })
})

// Model Profiles is the one NumberField surface with NO save button and NO busy
// gate: `patch` → `patchValues` → `save()` calls `updateModelProfile` on every
// commit, which rewrites ~/.c0wrk/model-profiles.yaml immediately. Nothing here
// drops the DOM focus either, so a commit that persisted a value the field never
// rendered is a silent, instant config write — the failure mode the shared
// NumberField's Enter handling must not reintroduce.
describe('ModelProfilesSettings — Context Management auto-save', () => {
  const numberField = (label: string): HTMLInputElement => {
    const el = field(label)
    expect(el).not.toBeNull()
    return el as HTMLInputElement
  }

  /** The `block_size` of every config the auto-save received, in order. */
  const savedBlockSizes = (): number[] =>
    updateModelProfileMock.mock.calls.map(
      (c) => (c[1] as { config: ModelProfileValues }).config.context.compaction.block_size,
    )

  it('persists only values it rendered, across an Enter and a following click-away', async () => {
    await render()
    const input = numberField('Block size')
    expect(input.value).toBe('5')

    // Focus, select-all, type, Enter. `type="number"` outside a <form> gives
    // Enter no default action, so the DOM node KEEPS focus after the commit.
    await focusIn(input)
    typeValue(input, '256')
    await pressKey(input, 'Enter')

    expect(input.value).toBe('256')
    expect(savedBlockSizes()).toEqual([256])

    // One more keystroke, still without a re-focus: it MUST be echoed. Before the
    // fix the field cleared its focus flag on Enter and rendered `String(value)`
    // from then on, so this text was invisible — and the click-away below
    // persisted it anyway.
    typeValue(input, '25')
    const shownBeforeBlur = input.value
    expect(shownBeforeBlur).toBe('25')

    // The real second half of the gesture: a bare `focusout`, no `focusin`.
    await focusOut(input)

    const saved = savedBlockSizes()
    expect(saved).toEqual([256, 25])
    // The auto-save received EXACTLY what was on screen when it was committed.
    expect(saved[saved.length - 1]).toBe(Number(shownBeforeBlur))
    expect(numberField('Block size').value).toBe('25')
  })

  it('refuses a decimal threshold locally — the auto-save never sees it', async () => {
    await render()
    const input = numberField('Block size')

    // `block_size` is an `int` on the wire; `step=1` constrains only the spinner
    // buttons, so typed text needs the field's own integrality check. Without it
    // 2.5 was written straight into model-profiles.yaml for Go to reject.
    await focusIn(input)
    typeValue(input, '2.5')
    await pressKey(input, 'Enter')

    expect(updateModelProfileMock).not.toHaveBeenCalled()
    expect(numberField('Block size').value).toBe('5')

    await focusOut(input)
    expect(updateModelProfileMock).not.toHaveBeenCalled()
  })
})
