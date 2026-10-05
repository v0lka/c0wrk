import { useState, useEffect, useCallback } from 'react'
import { Loader2, AlertTriangle, Copy, Pencil, Trash2 } from 'lucide-react'
import {
  getModelProfiles,
  updateModelProfile,
  createModelProfile,
  deleteModelProfile,
  selectModelProfile,
  setModelProfilesEnabled,
} from '@/api/config'
import { logger } from '@/lib/logger'
import { Button } from '@/components/ui/button'
import { Toggle } from './ModelProfilesControls'
import { ModelProfileSelector } from './ModelProfileSelector'
import { ModelProfileDialog, type ModelProfileDialogKind } from './ModelProfileDialog'
import {
  EssentialToolsSection,
  SystemPromptSection,
  SamplingSection,
  LoopHardeningSection,
} from './ModelProfilesSections'
import { ContextSection } from './ModelProfilesContextSection'
import type {
  ModelProfilesResponse,
  ModelProfileValues,
  ModelProfilesEssentialTools,
  ModelProfilesSystemPrompt,
  ModelProfilesSampling,
  ModelProfilesLoopHardening,
  ModelProfilesContext,
} from '@/types/models'

/** Frontend mirror of the backend generic fallback id (config.ModelProfilesGenericProfileID). */
const GENERIC_PROFILE_ID = 'generic'

/**
 * Model Profiles settings tab. A master toggle (config.yaml model_profiles.enabled) sits at the
 * top: while off, the whole profile UI below is hidden and only a short hint
 * explains how to turn it on. Turning it on persists + applies immediately via
 * SetModelProfilesEnabled and reveals the profile picker above the five variant sections.
 * Predefined profiles render read-only (values visible, controls disabled)
 * with a Duplicate action; custom profiles are fully editable with
 * Rename/Delete. The suggestion banner never auto-applies — Apply is an
 * explicit select, Hide dismisses until the suggested profile changes (a
 * default-model switch) or the app restarts. Neither the profile actions nor
 * this master toggle ever touch the experimental-features switch: that lives on
 * the General tab and controls only the E2S execution mode.
 */
export function ModelProfilesSettings() {
  const [resp, setResp] = useState<ModelProfilesResponse | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  /** Dismissed suggestion id: stays hidden until the suggestion (model) changes. */
  const [dismissedSuggestion, setDismissedSuggestion] = useState<string | null>(null)
  const [dialog, setDialog] = useState<ModelProfileDialogKind | null>(null)
  const [dialogDefaultName, setDialogDefaultName] = useState('')
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [openSections, setOpenSections] = useState({
    essential_tools: true,
    system_prompt: true,
    sampling: true,
    loop_hardening: true,
    context: true,
  })

  useEffect(() => {
    getModelProfiles()
      .then((r) => {
        setResp(r)
        setError(null)
      })
      .catch((err) => logger.error('Failed to load Model Profiles profiles:', err))
      .finally(() => setIsLoading(false))
  }, [])

  const reload = useCallback(async () => {
    const r = await getModelProfiles()
    setResp(r)
    setError(null)
  }, [])

  const active = resp ? (resp.profiles.find((p) => p.id === resp.active_id) ?? null) : null
  // A dangling active id (deleted/unknown profile) resolves to generic — the
  // same soft fallback the backend resolver applies — and is read-only.
  const effective =
    active ?? (resp ? (resp.profiles.find((p) => p.id === GENERIC_PROFILE_ID) ?? null) : null)
  const readOnly = !active || active.kind === 'predefined'

  // The backend reports a dangling active id BOTH through the dedicated fallback
  // banner below AND a `warnings` entry (the resolver's "not found … falling
  // back" message). While the banner is shown, suppress that matching entry so
  // the one condition is not rendered twice. Every other warning still renders
  // — store errors, one-shot notices, and the empty-active-profile notice the
  // banner does not cover.
  const fallbackBannerVisible = !active && resp !== null && resp.active_id !== ''
  const visibleWarnings =
    resp === null
      ? []
      : resp.warnings.filter(
          (w) =>
            !(
              fallbackBannerVisible &&
              w.includes(`"${resp.active_id}"`) &&
              w.includes('not found in the profile catalog')
            ),
        )

  const suggestedId = resp?.suggested_profile_id ?? null
  const suggestedName = suggestedId
    ? (resp?.profiles.find((p) => p.id === suggestedId)?.name ?? suggestedId)
    : null
  const suggestionVisible =
    resp !== null &&
    suggestedId !== null &&
    suggestedId !== resp.active_id &&
    dismissedSuggestion !== suggestedId

  /** Values of the rendered profile get patched only when it is editable. */
  const save = useCallback(
    async (next: ModelProfileValues) => {
      if (!resp || !active || active.kind !== 'custom') return
      setResp({
        ...resp,
        profiles: resp.profiles.map((p) => (p.id === active.id ? { ...p, values: next } : p)),
      })
      try {
        await updateModelProfile(active.id, { config: next })
        setError(null)
      } catch (err) {
        logger.error('Failed to update Model Profiles profile:', err)
        setError(err instanceof Error ? err.message : String(err))
        try {
          setResp(await getModelProfiles())
        } catch {
          /* keep local state if reload also fails */
        }
      }
    },
    [active, resp],
  )

  const patchValues = (mutate: (v: ModelProfileValues) => ModelProfileValues) => {
    if (effective && !readOnly) save(mutate(effective.values))
  }

  /** Wrap a profile RPC: disable the controls, reload the catalog after. */
  const runProfileOp = async (op: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    try {
      await op()
      await reload()
    } catch (err) {
      logger.error('Model Profiles profile operation failed:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  /**
   * Master switch (config.yaml model_profiles.enabled): persist + apply, then reload the
   * catalog so the profile UI reflects the new state. On failure the error is
   * surfaced and the toggle reverts. This NEVER touches the experimental-features
   * switch — that gates E2S and the embedded model's frontend surfaces, and is
    * owned by the General tab.
   */
  const handleToggleEnabled = async (next: boolean) => {
    if (busy) return
    setBusy(true)
    try {
      await setModelProfilesEnabled(next)
      await reload()
    } catch (err) {
      logger.error('Failed to toggle Model Profiles enabled:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const handleSelect = (id: string) => {
    if (!resp || id === resp.active_id) return
    void runProfileOp(() => selectModelProfile(id))
  }

  const openDialog = (kind: ModelProfileDialogKind) => {
    setDialogError(null)
    setDialogDefaultName(
      kind === 'duplicate' && effective ? `${effective.name} (copy)` : (active?.name ?? ''),
    )
    setDialog(kind)
  }

  const closeDialog = () => {
    if (busy) return
    setDialog(null)
    setDialogError(null)
  }

  const handleDialogConfirm = async (name: string) => {
    if (!effective || !dialog) return
    setBusy(true)
    setDialogError(null)
    try {
      if (dialog === 'duplicate') {
        // Duplicate then activate the copy, so the user lands in an editable
        // form of the values they were just viewing.
        const newId = await createModelProfile(effective.id, name)
        await selectModelProfile(newId)
      } else if (dialog === 'rename' && active) {
        await updateModelProfile(active.id, { name })
      } else if (dialog === 'delete' && active) {
        await deleteModelProfile(active.id)
      }
      await reload()
      setDialog(null)
    } catch (err) {
      logger.error('Model Profiles profile dialog operation failed:', err)
      setDialogError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const setSection = (key: keyof typeof openSections, open: boolean) =>
    setOpenSections((s) => ({ ...s, [key]: open }))

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8 gap-2">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading Model Profiles settings...</span>
      </div>
    )
  }

  if (!resp) {
    return (
      <div className="flex flex-col items-center justify-center py-8 gap-2">
        <span className="text-sm text-muted-foreground">Model Profiles profile is unavailable.</span>
        {error && <span className="text-xs text-destructive">{error}</span>}
      </div>
    )
  }

  const enabled = resp.enabled

  // Master toggle (config.yaml model_profiles.enabled): the first element, always visible.
  // It gates the whole profile UI below. This is NOT the experimental-features
  // switch, which gates E2S and the embedded model's frontend surfaces and
   // lives on the General tab.
  const masterToggle = (
    <div className="flex flex-col gap-2 p-4 rounded-lg border border-border bg-card/50">
      <Toggle
        checked={enabled}
        onChange={handleToggleEnabled}
        disabled={busy}
        label="Enable the Model Profiles profile"
        description="Apply the selected profile's tool, prompt, sampling, loop and context overrides to Model Profiles runs. When off, none of the profile settings below take effect."
      />
      {busy && (
        <span className="flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          Applying…
        </span>
      )}
    </div>
  )

  // Master switch off: only the toggle and a short hint — every profile control
  // (picker, CRUD actions, banners) and the five variant sections stay hidden.
  if (!enabled) {
    return (
      <div className="flex flex-col gap-4">
        {masterToggle}
        <p className="text-xs text-muted-foreground">
          The Model Profiles profile is off. Turn it on to configure and apply a profile.
        </p>
        {error && (
          <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
            <span className="text-destructive">{error}</span>
          </div>
        )}
      </div>
    )
  }

  if (!effective) {
    return (
      <div className="flex flex-col gap-4">
        {masterToggle}
        <div className="flex flex-col items-center justify-center py-8">
          <span className="text-sm text-muted-foreground">Model Profiles profile is unavailable.</span>
        </div>
      </div>
    )
  }

  // The essential-tools form view: rendered profile values + picker universe.
  const essentialView: ModelProfilesEssentialTools = {
    ...effective.values.essential_tools,
    protected_tools: resp.protected_tools,
    builtin_tools: resp.builtin_tools,
    tool_groups: resp.tool_groups,
  }

  return (
    <div className="flex flex-col gap-4">
      {masterToggle}
      {/* Profile block: grouped picker + kind-scoped actions. */}
      <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
        <h3 className="text-sm font-medium">Active profile</h3>
        <div className="flex items-center gap-3 flex-wrap">
          <ModelProfileSelector
            profiles={resp.profiles}
            activeId={resp.active_id}
            onSelect={handleSelect}
            disabled={busy}
          />
          <span className="text-xs text-muted-foreground">
            {!active ? `unknown (using ${effective.name})` : active.kind}
          </span>
          <div className="ml-auto flex items-center gap-2">
            {readOnly ? (
              <Button
                size="sm"
                variant="outline"
                onClick={() => openDialog('duplicate')}
                disabled={busy}
                aria-label="Duplicate profile"
              >
                <Copy className="h-3.5 w-3.5" />
                Duplicate
              </Button>
            ) : (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => openDialog('rename')}
                  disabled={busy}
                  aria-label="Rename profile"
                >
                  <Pencil className="h-3.5 w-3.5" />
                  Rename
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() => openDialog('delete')}
                  disabled={busy}
                  aria-label="Delete profile"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  Delete
                </Button>
              </>
            )}
          </div>
        </div>
        {readOnly && (
          <p className="text-xs text-muted-foreground">
            Predefined profiles are read-only — duplicate this profile as a custom one to edit its
            values.
          </p>
        )}
        {suggestionVisible && (
          <div
            data-testid="model_profiles-suggestion-banner"
            role="status"
            className="flex items-center gap-3 flex-wrap p-2 rounded-md bg-primary/10 border border-primary/20 text-sm"
          >
            <span>
              A profile matching your default model is available:{' '}
              <span className="font-medium">{suggestedName}</span>.
            </span>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => suggestedId && handleSelect(suggestedId)}
                disabled={busy}
                aria-label="Apply suggested profile"
              >
                Apply
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => suggestedId && setDismissedSuggestion(suggestedId)}
                disabled={busy}
                aria-label="Dismiss suggested profile"
              >
                Hide
              </Button>
            </div>
          </div>
        )}
        {fallbackBannerVisible && (
          <div
            data-testid="model_profiles-fallback-warning"
            className="flex items-start gap-2 p-2 rounded-md bg-warning/10 border border-warning/20 text-xs"
          >
            <AlertTriangle className="h-3 w-3 text-warning flex-shrink-0 mt-0.5" />
            <span>
              Active profile “{resp.active_id}” was not found — the effective profile is{' '}
              {effective.name} until you pick another one.
            </span>
          </div>
        )}
        {visibleWarnings.length > 0 && (
          <div className="flex flex-col gap-1">
            {visibleWarnings.map((w) => (
              <div
                key={w}
                className="flex items-start gap-2 p-2 rounded-md bg-warning/10 border border-warning/20 text-xs"
              >
                <AlertTriangle className="h-3 w-3 text-warning flex-shrink-0 mt-0.5" />
                <span>{w}</span>
              </div>
            ))}
          </div>
        )}
        {error && (
          <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
            <span className="text-destructive">{error}</span>
          </div>
        )}
      </div>

      {/* The five variant sections render for every profile kind; for
          predefined ones every control is disabled (read-only view). */}
      <EssentialToolsSection
        slice={essentialView}
        patch={(p: Partial<ModelProfilesEssentialTools>) =>
          patchValues((v) => ({ ...v, essential_tools: { ...v.essential_tools, ...p } }))
        }
        open={openSections.essential_tools}
        onOpenChange={(o) => setSection('essential_tools', o)}
        disabled={readOnly}
      />
      <SystemPromptSection
        slice={effective.values.system_prompt}
        patch={(p: Partial<ModelProfilesSystemPrompt>) =>
          patchValues((v) => ({ ...v, system_prompt: { ...v.system_prompt, ...p } }))
        }
        open={openSections.system_prompt}
        onOpenChange={(o) => setSection('system_prompt', o)}
        disabled={readOnly}
      />
      <SamplingSection
        slice={effective.values.sampling}
        patch={(p: Partial<ModelProfilesSampling>) => patchValues((v) => ({ ...v, sampling: { ...v.sampling, ...p } }))}
        open={openSections.sampling}
        onOpenChange={(o) => setSection('sampling', o)}
        disabled={readOnly}
      />
      <LoopHardeningSection
        slice={effective.values.loop_hardening}
        patch={(p: Partial<ModelProfilesLoopHardening>) =>
          patchValues((v) => ({ ...v, loop_hardening: { ...v.loop_hardening, ...p } }))
        }
        open={openSections.loop_hardening}
        onOpenChange={(o) => setSection('loop_hardening', o)}
        disabled={readOnly}
      />
      <ContextSection
        slice={effective.values.context}
        patch={(p: Partial<ModelProfilesContext>) => patchValues((v) => ({ ...v, context: { ...v.context, ...p } }))}
        open={openSections.context}
        onOpenChange={(o) => setSection('context', o)}
        disabled={readOnly}
      />

      {dialog && (
        <ModelProfileDialog
          kind={dialog}
          profileName={effective.name}
          defaultName={dialogDefaultName}
          busy={busy}
          error={dialogError}
          onConfirm={handleDialogConfirm}
          onClose={closeDialog}
        />
      )}
    </div>
  )
}
