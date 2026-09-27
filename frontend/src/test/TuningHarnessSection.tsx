// The section the shared tuning test harness mounts (./tuningTestHarness).
//
// Its own file because eslint-plugin-react-refresh requires a module that
// declares a component to export nothing else, and the harness exports its
// helpers. Rendering the variant through a prop (instead of a module-level flag
// the harness mutates) keeps this component pure.

import { EmbeddedLLMTuning } from '@/components/settings/embedded/EmbeddedLLMTuning'
import { EmbeddedLLMAdvancedTuning } from '@/components/settings/embedded/EmbeddedLLMAdvancedTuning'
import { useEmbeddedLLMTuning } from '@/hooks/useEmbeddedLLMTuning'
import type { TuningHarnessVariant } from './tuningTestHarness'

/** Mount the real hook and hand the matching prop bundle to its section. */
export function TuningHarnessSection({ variant }: { variant: TuningHarnessVariant }) {
  const tuning = useEmbeddedLLMTuning()
  return variant === 'primary' ? (
    <EmbeddedLLMTuning {...tuning.primary} />
  ) : (
    <EmbeddedLLMAdvancedTuning {...tuning.advanced} />
  )
}
