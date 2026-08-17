import { useState, useEffect, useCallback } from 'react'
import { Loader2, AlertTriangle } from 'lucide-react'
import { getSmallLLMConfig, updateSmallLLMConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { Toggle } from './SmallLLMControls'
import {
  EssentialToolsSection,
  SystemPromptSection,
  SamplingSection,
  LoopHardeningSection,
} from './SmallLLMSections'
import { ContextSection } from './SmallLLMContextSection'
import type {
  SmallLLMConfigResponse,
  SmallLLMEssentialTools,
  SmallLLMSystemPrompt,
  SmallLLMSampling,
  SmallLLMLoopHardening,
  SmallLLMContext,
} from '@/types/models'

export function SmallLLMSettings() {
  const [config, setConfig] = useState<SmallLLMConfigResponse | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [openSections, setOpenSections] = useState({
    essential_tools: true,
    system_prompt: true,
    sampling: true,
    loop_hardening: true,
    context: true,
  })

  useEffect(() => {
    getSmallLLMConfig()
      .then((r) => {
        setConfig(r)
        setError(null)
      })
      .catch((err) => logger.error('Failed to load Small LLM config:', err))
      .finally(() => setIsLoading(false))
  }, [])

  // Persist the full updated config; revert to the reloaded value on failure
  // so a rejected (invalid) change does not linger in the local UI.
  const save = useCallback(async (next: SmallLLMConfigResponse) => {
    setConfig(next)
    try {
      await updateSmallLLMConfig(next)
      setError(null)
    } catch (err) {
      logger.error('Failed to update Small LLM config:', err)
      setError(err instanceof Error ? err.message : 'Failed to save Small LLM settings')
      try {
        setConfig(await getSmallLLMConfig())
      } catch {
        /* keep local state if reload also fails */
      }
    }
  }, [])

  const setSection = (key: keyof typeof openSections, open: boolean) =>
    setOpenSections((s) => ({ ...s, [key]: open }))

  const patchEssentialTools = (p: Partial<SmallLLMEssentialTools>) => {
    if (config) save({ ...config, essential_tools: { ...config.essential_tools, ...p } })
  }
  const patchSystemPrompt = (p: Partial<SmallLLMSystemPrompt>) => {
    if (config) save({ ...config, system_prompt: { ...config.system_prompt, ...p } })
  }
  const patchSampling = (p: Partial<SmallLLMSampling>) => {
    if (config) save({ ...config, sampling: { ...config.sampling, ...p } })
  }
  const patchLoopHardening = (p: Partial<SmallLLMLoopHardening>) => {
    if (config) save({ ...config, loop_hardening: { ...config.loop_hardening, ...p } })
  }
  const patchContext = (p: Partial<SmallLLMContext>) => {
    if (config) save({ ...config, context: { ...config.context, ...p } })
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8 gap-2">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading Small LLM settings...</span>
      </div>
    )
  }

  if (!config) {
    return (
      <div className="flex flex-col items-center justify-center py-8 gap-2">
        <span className="text-sm text-muted-foreground">Small LLM profile is unavailable.</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Master toggle */}
      <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
        <Toggle
          checked={config.enabled}
          onChange={(enabled) => save({ ...config, enabled })}
          label="Enable Small LLM profile"
          description="When enabled, this profile with all its options applies to the current active model — intended for use with smaller, cheaper models."
        />
        {error && (
          <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
            <span className="text-destructive">{error}</span>
          </div>
        )}
      </div>

      {/* Variants are inert unless the master toggle is on — hide them entirely
          until the profile is enabled. */}
      {config.enabled ? (
        <>
          <EssentialToolsSection
            slice={config.essential_tools}
            patch={patchEssentialTools}
            open={openSections.essential_tools}
            onOpenChange={(o) => setSection('essential_tools', o)}
          />
          <SystemPromptSection
            slice={config.system_prompt}
            patch={patchSystemPrompt}
            open={openSections.system_prompt}
            onOpenChange={(o) => setSection('system_prompt', o)}
          />
          <SamplingSection
            slice={config.sampling}
            patch={patchSampling}
            open={openSections.sampling}
            onOpenChange={(o) => setSection('sampling', o)}
          />
          <LoopHardeningSection
            slice={config.loop_hardening}
            patch={patchLoopHardening}
            open={openSections.loop_hardening}
            onOpenChange={(o) => setSection('loop_hardening', o)}
          />
          <ContextSection
            slice={config.context}
            patch={patchContext}
            open={openSections.context}
            onOpenChange={(o) => setSection('context', o)}
          />
        </>
      ) : (
        <p className="text-xs text-muted-foreground px-1">
          Enable the profile to configure its variants. Every variant is ignored while this is off.
        </p>
      )}
    </div>
  )
}
