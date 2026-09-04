import { useResearchStatusEvents } from '@/hooks/useResearchStatusEvents'
import { useResearchFileWatcher } from '@/hooks/useResearchFileWatcher'

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
  return null
}
