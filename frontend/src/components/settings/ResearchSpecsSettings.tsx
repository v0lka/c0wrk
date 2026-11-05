import { useCallback, useEffect, useState } from 'react'
import { PanelsTopLeft } from 'lucide-react'
import { getConfig } from '@/api/config'
import { setResearchEnabled, setSpecsEnabled } from '@/api/sidebarPacks'
import { useSidebarPacksStore } from '@/stores/sidebarPacksStore'
import { logger } from '@/lib/logger'
import { Toggle } from './ModelProfilesControls'

/**
 * General-tab control for the sidebar surface gates (config
 * `research.enabled` / `specs.enabled`), mirroring PromptSuggestionsSettings
 * above it. Each gate decides whether its sidebar tab (Research / Specs in
 * the workspace panel) renders at all AND whether the surface's
 * globally-seeded skill/agent pack stays on disk: disabling hides the tab
 * and unseeds the pack copies; enabling reseeds them. Both effects run in
 * the backend toggle RPC — no restart.
 *
 * The values live in backend config (nil default = on), so the section reads
 * the effective gates through GetConfig on mount and writes through
 * SetResearchEnabled / SetSpecsEnabled — which persist AND apply in the same
 * call. Writes are optimistic with a rollback: a rejected write leaves the
 * previous value authoritative everywhere, so the control snaps back to it.
 * A successful save also updates the sidebarPacksStore directly so the
 * workspace tabs flip in the same beat as the persisted config.
 */

type GateKey = 'research' | 'specs'

export function ResearchSpecsSettings() {
  // null until the config answers — the toggles stay disabled rather than
  // showing a guessed state.
  const [gates, setGates] = useState<Record<GateKey, boolean | null>>({
    research: null,
    specs: null,
  })
  const [saving, setSaving] = useState<GateKey | null>(null)

  useEffect(() => {
    let cancelled = false
    void getConfig()
      .then((cfg) => {
        if (!cancelled) {
          setGates({
            research: cfg.research?.enabled ?? true,
            specs: cfg.specs?.enabled ?? true,
          })
        }
      })
      .catch((err) => logger.warn('[sidebar-packs] gate read failed', err))
    return () => {
      cancelled = true
    }
  }, [])

  const handleChange = useCallback(async (key: GateKey, next: boolean) => {
    const current = gates[key]
    if (current === null) return
    const previous = current
    setGates((g) => ({ ...g, [key]: next })) // optimistic: must feel immediate
    setSaving(key)
    try {
      if (key === 'research') {
        await setResearchEnabled(next)
      } else {
        await setSpecsEnabled(next)
      }
      // The workspace tabs follow the store directly — the persisted config
      // is already the authority the store hydrates from, so this stays in
      // sync with (and ahead of) the config:updated re-hydration.
      if (key === 'research') {
        useSidebarPacksStore.getState().setResearch(next)
      } else {
        useSidebarPacksStore.getState().setSpecs(next)
      }
    } catch (err) {
      logger.error(`Failed to toggle ${key}:`, err)
      setGates((g) => ({ ...g, [key]: previous })) // the backend holds the previous value
    } finally {
      setSaving(null)
    }
  }, [gates])

  return (
    <div className="flex flex-col gap-2" data-testid="research-specs-settings">
      <div className="flex items-center gap-2">
        <PanelsTopLeft className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Sidebar Research &amp; Specs</span>
      </div>
      <Toggle
        checked={gates.research ?? false}
        onChange={(next) => void handleChange('research', next)}
        disabled={gates.research === null || saving !== null}
        label="Research"
        description="Show the Research sidebar tab and keep its seeded skills and agents on disk (the research-* methodology skills, study-paper, the research agent). Disabling hides the tab and removes the seeded copies; locally edited copies are kept. The change applies immediately."
      />
      <Toggle
        checked={gates.specs ?? false}
        onChange={(next) => void handleChange('specs', next)}
        disabled={gates.specs === null || saving !== null}
        label="Specs"
        description="Show the Specs sidebar tab and keep its seeded skills and agent on disk (the spec-* skills, the specs agent). Disabling hides the tab and removes the seeded copies; locally edited copies are kept. The change applies immediately."
      />
    </div>
  )
}
