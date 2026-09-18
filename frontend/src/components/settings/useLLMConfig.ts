import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { useProxyDraftStore, selectProxyActive, isProxyEffective } from '@/stores/proxyDraftStore'
import { FIXED_PROVIDERS, type CompatibleType } from '@/lib/llm-providers'
import { compositeModelId, isCompositeModelId, decomposeCompositeModelId } from '@/lib/modelId'
import type { ConfigProviderFull } from '@/types/models'
import { useLLMConfigSave } from './useLLMConfigSave'

export interface ProviderConfig {
    api_key: string
    base_url: string
    models: string[]
    /**
     * Transport type for compatible (named, custom-endpoint) providers.
     * Fixed providers (anthropic, chatgpt) leave this undefined; it is only
     * meaningful for compatible providers and drives which backend map
     * (openai_compatible vs anthropic_compatible) they are saved under.
     */
    type?: CompatibleType
    /** Per-provider TLS pin (ADR-052): '' = standard CA verification
     *  (override off), non-empty = only the pinned key is accepted. */
    tls_fingerprint: string
}

const defaultProviderConfigs: Record<string, ProviderConfig> = Object.fromEntries(
    FIXED_PROVIDERS.map((p) => [p, { api_key: '', base_url: '', models: [], tls_fingerprint: '' }]),
)

interface UseLLMConfigResult {
    defaultModel: string
    providerConfigs: Record<string, ProviderConfig>
    /** Names of providers loaded from the openai_compatible map (non-fixed providers). */
    openaiCompatibleProviderNames: Set<string>
    /** Names of providers loaded from the anthropic_compatible map. */
    anthropicCompatibleProviderNames: Set<string>
    /** Effective proxy state (enabled AND a URL set), mirroring the Go-side
     *  proxy.BuildTransport rule. The per-provider TLS pin is inert while
     *  this is true (proxy wins, ADR-052), so the pin UI is disabled. Read
     *  from proxyDraftStore, which the General tab updates synchronously —
     *  see that store for why a config re-read cannot serve this. */
    proxyActive: boolean
    isLoading: boolean
    setDefaultModel: (model: string) => void
    updateProviderConfig: (provider: string, updates: Partial<ProviderConfig>) => void
    toggleModel: (provider: string, model: string) => void
    addProvider: (name: string, config: ProviderConfig) => void
    deleteProvider: (name: string) => void
}

function toProviderConfig(p: ConfigProviderFull, type?: CompatibleType): ProviderConfig {
    return {
        api_key: p.api_key,
        base_url: p.base_url ?? '',
        models: Array.isArray(p.models) ? [...p.models] : [],
        type,
        tls_fingerprint: p.tls_fingerprint ?? '',
    }
}

/**
 * Normalize a `default_model` value to its composite "provider/name" form so
 * the rest of the settings dialog can compare against composite identifiers
 * unambiguously (the "default" badge, delete-confirmation ownership, and the
 * default-model dropdown all key off composite ids).
 *
 * - Already composite ("provider/name"): returned unchanged.
 * - Bare model name: resolved to the first provider that exposes it. The
 *   `configs` object is built by {@link loadConfig} with fixed providers
 *   (anthropic, chatgpt) inserted first, then compatible providers whose JSON
 *   keys are alphabetically sorted by the backend — so `Object.entries`
 *   iteration order mirrors the backend's `allProviderEntries` order, and the
 *   first match wins just like `config.LLMConfig.ResolveDefaultModelProvider`.
 * - Stale bare name not enabled in any provider: returned unchanged so the
 *   dropdown shows its placeholder and no badge is rendered.
 */
function normalizeDefaultModel(
    def: string,
    configs: Record<string, ProviderConfig>,
): string {
    if (!def || isCompositeModelId(def)) return def
    for (const [provider, cfg] of Object.entries(configs)) {
        if (cfg?.models.includes(def)) {
            return compositeModelId(provider, def)
        }
    }
    return def
}

/**
 * Return true when `defaultModel` resolves to a model currently enabled in
 * some provider within `configs`. Mirrors the backend invariant: a default
 * must resolve to an enabled model. Used to self-clear a dangling default
 * after a provider is deleted or its backing model is disabled.
 *
 * - Composite "provider/name": the named provider must enable the model.
 * - Bare name: any provider enabling it counts (first match wins).
 *
 * Exported for direct unit testing of the reconciliation semantics.
 */
export function defaultModelIsValid(
    defaultModel: string,
    configs: Record<string, ProviderConfig>,
): boolean {
    if (!defaultModel) return false
    const parts = decomposeCompositeModelId(defaultModel)
    if (parts) {
        return configs[parts.provider]?.models.includes(parts.model) ?? false
    }
    return Object.values(configs).some((cfg) => cfg?.models.includes(defaultModel))
}

/**
 * Manages LLM config state: loading, default model, per-provider config, and model toggling.
 * Persistence is delegated to useLLMConfigSave (fixes #1, #7, #8).
 */
export function useLLMConfig(onSettingsSaved?: () => void, onDefaultModelChange?: (model: string) => void): UseLLMConfigResult {
    const [defaultModel, setDefaultModelState] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [openaiCompatibleProviderNames, setOpenaiCompatibleProviderNames] = useState<Set<string>>(new Set())
    const [anthropicCompatibleProviderNames, setAnthropicCompatibleProviderNames] = useState<Set<string>>(new Set())
    const [isLoading, setIsLoading] = useState(true)

    // The pin UI gate. proxyDraftStore is authoritative because the General
    // tab writes it synchronously on every edit; loadConfig only SEEDS it
    // (a no-op once known) for the case where the LLM tab is opened first
    // and the General tab never mounted.
    const proxyActive = useProxyDraftStore(selectProxyActive)
    const seedProxyActive = useProxyDraftStore((s) => s.seedActive)

    // Mutable ref for providerConfigs so setDefaultModel stays stable (fix #5).
    const configsRef = useRef(providerConfigs)
    configsRef.current = providerConfigs

    // Mutable ref for the onDefaultModelChange callback so loadConfig (and the
    // provider mutators below) stay stable while still reporting the effective
    // default to the parent on every change — including load, delete, and toggle.
    const onDefaultModelChangeRef = useRef(onDefaultModelChange)
    onDefaultModelChangeRef.current = onDefaultModelChange

    const { saveFullConfig, debouncedSave, cancelDebouncedSave, buildSafeUpdates } = useLLMConfigSave(onSettingsSaved)

    const persistWhenDefaultIsValid = useCallback((model: string, configs: Record<string, ProviderConfig>) => {
        if (!defaultModelIsValid(model, configs)) {
            // Keep the edited provider list locally until the user selects an
            // enabled replacement default. This also cancels a timer queued
            // before the default was removed, preventing an invalid RPC.
            cancelDebouncedSave()
            return
        }
        debouncedSave(model, configs)
    }, [cancelDebouncedSave, debouncedSave])

    /**
     * Immediate variant of {@link persistWhenDefaultIsValid} for structural
     * mutations (add/delete provider). The debounced path cancels its timer
     * on unmount, and these mutations are followed by exactly that — closing
     * the settings dialog or switching its tab unmounts LLMSettings — so a
     * debounced save made within 300 ms of the click would be silently
     * dropped. Saving synchronously closes that window; cancel any queued
     * timer so its stale pre-mutation snapshot cannot land after this save,
     * and hold the change locally (no RPC) until a valid default exists.
     */
    const persistImmediatelyWhenDefaultIsValid = useCallback((model: string, configs: Record<string, ProviderConfig>) => {
        // Cancel any queued debounce timer on BOTH branches. The timer
        // captures its (model, configs) arguments when it is queued, so a
        // timer queued by an edit within the previous 300 ms holds the
        // PRE-mutation snapshot and would otherwise fire AFTER this immediate
        // save — silently reverting the structural change (a deleted provider
        // resurrected / a just-added provider erased) on the backend while the
        // UI still shows the new state. The immediate save carries the fresh
        // configs, so dropping the timer loses nothing.
        cancelDebouncedSave()
        if (!defaultModelIsValid(model, configs)) {
            return
        }
        saveFullConfig(model, configs)
    }, [cancelDebouncedSave, saveFullConfig])

    const loadConfig = useCallback(async () => {
        try {
            const result = await getConfig()
            // Seed the pin UI gate from the same payload the provider
            // configs come from. seedActive never overwrites a value the
            // General tab already published, so a draft toggle wins.
            seedProxyActive(isProxyEffective(result?.proxy))
            const llm = result?.llm
            if (llm) {
                const rawDefault = llm.default_model || ''
                const configs: Record<string, ProviderConfig> = {}
                const openaiNames = new Set<string>()
                const anthropicNames = new Set<string>()

                // Load fixed providers (anthropic, chatgpt).
                for (const p of FIXED_PROVIDERS) {
                    const cfg = llm[p]
                    if (cfg) {
                        configs[p] = toProviderConfig(cfg)
                    }
                }

                // Load openai_compatible providers from the map.
                const ocProviders = llm.openai_compatible
                if (ocProviders && typeof ocProviders === 'object') {
                    for (const [name, cfg] of Object.entries(ocProviders)) {
                        configs[name] = toProviderConfig(cfg, 'openai')
                        openaiNames.add(name)
                    }
                }

                // Load anthropic_compatible providers from the map.
                const acProviders = llm.anthropic_compatible
                if (acProviders && typeof acProviders === 'object') {
                    for (const [name, cfg] of Object.entries(acProviders)) {
                        configs[name] = toProviderConfig(cfg, 'anthropic')
                        anthropicNames.add(name)
                    }
                }

                // Normalize the default model to its composite "provider/name"
                // form so every comparison in the dialog (badge, delete
                // confirmation, dropdown) keys off composite ids consistently.
                // Then validate against the loaded providers: a stale default
                // pointing at a removed/disabled model is treated as empty so
                // the settings dialog can block close until a new one is picked.
                const normalized = normalizeDefaultModel(rawDefault, configs)
                const effective = defaultModelIsValid(normalized, configs) ? normalized : ''
                setDefaultModelState(effective)
                setProviderConfigs(configs)
                setOpenaiCompatibleProviderNames(openaiNames)
                setAnthropicCompatibleProviderNames(anthropicNames)
                onDefaultModelChangeRef.current?.(effective)
            } else {
                setDefaultModelState('')
                setProviderConfigs({ ...defaultProviderConfigs })
                setOpenaiCompatibleProviderNames(new Set())
                setAnthropicCompatibleProviderNames(new Set())
                onDefaultModelChangeRef.current?.('')
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            // Preserve the last successfully loaded provider state. A transient
            // or validation error must not make custom providers disappear.
        } finally {
            setIsLoading(false)
        }
    }, [seedProxyActive])

    useEffect(() => { loadConfig() }, [loadConfig])

    const setDefaultModel = useCallback((model: string) => {
        setDefaultModelState(model)
        onDefaultModelChangeRef.current?.(model)
        persistWhenDefaultIsValid(model, configsRef.current)
    }, [persistWhenDefaultIsValid])

    const updateProviderConfig = useCallback((provider: string, updates: Partial<ProviderConfig>) => {
        // Compute next state outside the setState updater so side effects
        // (debouncedSave → RPC) are not invoked during render. React may run a
        // state updater more than once (notably StrictMode in dev), which would
        // otherwise duplicate the save RPC. We read the latest committed state
        // via configsRef instead.
        const prev = configsRef.current
        const existing = prev[provider]
        if (!existing) return
        const safeUpdates = buildSafeUpdates(existing, updates)
        const updated = { ...existing, ...safeUpdates }
        const next = { ...prev, [provider]: updated }
        setProviderConfigs(next)
        persistWhenDefaultIsValid(defaultModel, next)
    }, [defaultModel, persistWhenDefaultIsValid, buildSafeUpdates])

    const toggleModel = useCallback((provider: string, model: string) => {
        // Compute next state outside the setState updater so side effects are
        // not invoked during render (React may run a state updater more than
        // once, notably StrictMode in dev, which would otherwise duplicate the
        // save RPC and the parent default-model notification).
        const prev = configsRef.current
        const existing = prev[provider]
        if (!existing) return
        const enabling = !existing.models.includes(model)
        const models = enabling
            ? [...existing.models, model]
            : existing.models.filter((m) => m !== model)
        const updated = { ...existing, models }
        const next = { ...prev, [provider]: updated }
        // If disabling this model invalidated the current default, clear it
        // so the dialog blocks close until a new default is picked. The
        // backend re-validation (UpdateLLMConfig) is a second line of
        // defense; doing it here keeps local UI state in sync immediately.
        let effectiveDefault = defaultModelIsValid(defaultModel, next) ? defaultModel : ''
        // First-run / empty default: enabling a model also claims it as the
        // default so provider credentials and the model list can persist
        // without a separate trip to the Default Model picker. Without this,
        // Fetch Models works but "Add provider" appears stuck — drafts never
        // reach config.yaml while default_model is unset.
        if (!effectiveDefault && enabling) {
            effectiveDefault = compositeModelId(provider, model)
        }
        setProviderConfigs(next)
        if (effectiveDefault !== defaultModel) {
            setDefaultModelState(effectiveDefault)
            onDefaultModelChangeRef.current?.(effectiveDefault)
        }
        persistWhenDefaultIsValid(effectiveDefault, next)
    }, [defaultModel, persistWhenDefaultIsValid])

    const addProvider = useCallback((name: string, config: ProviderConfig) => {
        // Compute next state outside the setState updater so the save RPC is
        // not invoked during render (React may run a state updater more than
        // once, notably StrictMode in dev).
        const next = { ...configsRef.current, [name]: config }
        setProviderConfigs(next)
        // Structural change — persist immediately (not debounced): the
        // debounce is cancelled on unmount, and the settings dialog closes /
        // switches tabs right after this action.
        persistImmediatelyWhenDefaultIsValid(defaultModel, next)
        // Track the compatible provider under the correct transport set so it
        // is saved to the right backend map (openai_compatible vs anthropic_compatible).
        if (config.type === 'anthropic') {
            setAnthropicCompatibleProviderNames((prev) => new Set(prev).add(name))
        } else {
            setOpenaiCompatibleProviderNames((prev) => new Set(prev).add(name))
        }
    }, [defaultModel, persistImmediatelyWhenDefaultIsValid])

    const deleteProvider = useCallback((name: string) => {
        // Compute next state outside the setState updater so persistence and
        // parent default-model notification are not invoked during render
        // (React may run a state updater more than once, notably StrictMode in
        // dev, which would otherwise duplicate the save RPC).
        const next = { ...configsRef.current }
        delete next[name]
        // If the deleted provider owned the default, clear it so the
        // dialog blocks close until a new default is picked. The backend
        // re-validation (UpdateLLMConfig) mirrors this; doing it here
        // keeps local UI state in sync immediately.
        const effectiveDefault = defaultModelIsValid(defaultModel, next) ? defaultModel : ''
        setProviderConfigs(next)
        if (effectiveDefault !== defaultModel) {
            setDefaultModelState(effectiveDefault)
            onDefaultModelChangeRef.current?.(effectiveDefault)
        }
        // Structural change — persist immediately (not debounced): the
        // debounce is cancelled on unmount, and the settings dialog closes /
        // switches tabs right after this action. Deleting the LAST compatible
        // provider relies on the explicit empty-map semantics in the request.
        persistImmediatelyWhenDefaultIsValid(effectiveDefault, next)
        setOpenaiCompatibleProviderNames((prev) => {
            const next = new Set(prev)
            next.delete(name)
            return next
        })
        setAnthropicCompatibleProviderNames((prev) => {
            const next = new Set(prev)
            next.delete(name)
            return next
        })
    }, [defaultModel, persistImmediatelyWhenDefaultIsValid])

    return {
        defaultModel,
        providerConfigs,
        openaiCompatibleProviderNames,
        anthropicCompatibleProviderNames,
        proxyActive,
        isLoading,
        setDefaultModel,
        updateProviderConfig,
        toggleModel,
        addProvider,
        deleteProvider,
    }
}
