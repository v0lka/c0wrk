import { useEffect } from 'react'
import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'

/**
 * Research store sync bridge: the data side-effect hooks (full status sync +
 * incremental graph updates) mounted exactly ONCE at the App root, gated on
 * the experimental feature (see App.tsx). ResearchPanel and ResearchWorkspace
 * are pure views over researchStore and must not mount these hooks
 * themselves — with the sidebar panel and the workspace tab visible at the
 * same time, every subscription, research-scoped watchdog, and fallback full
 * refetch used to run twice. Returns null — renders no DOM.
 */
export function ResearchEventBridge() {
  useResearchStatusEvents()
  useResearchFileWatcher()

  // The bridge unmounts exactly when the experimental switch flips off (App
  // gates it on the same store value, which config:updated keeps in sync via
  // useExperimentalFeatures). An open research viewer tab must not outlive
  // that transition: its workspace would render frozen store data and its
  // Save action would fire research mutations against a disabled feature.
  // The purge lives here — not in the viewer — so it fires regardless of
  // whether the file viewer is currently mounted or collapsed.
  useEffect(
    () => () => {
      const { openTabs, closeFile } = useFileViewerStore.getState()
      if (openTabs.includes(RESEARCH_TAB_PATH)) closeFile(RESEARCH_TAB_PATH)
    },
    [],
  )

  return null
}
