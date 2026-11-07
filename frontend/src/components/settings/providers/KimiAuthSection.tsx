import { useKimiAuthFlow } from '@/hooks/useKimiAuthFlow'
import type { KimiAuthMode, KimiAuthRegion } from '@/types/models'
import { ProviderAuthSection } from './ProviderAuthSection'

interface KimiAuthSectionProps {
  authMode: KimiAuthMode
  onAuthModeChange: (mode: KimiAuthMode) => void
  authRegion?: KimiAuthRegion
  onAuthRegionChange?: (region: KimiAuthRegion) => void
  beforeSignIn?: () => Promise<void>
}

export function KimiAuthSection({ authMode, onAuthModeChange, authRegion = 'global', onAuthRegionChange = () => {}, beforeSignIn }: KimiAuthSectionProps) {
  const flow = useKimiAuthFlow(beforeSignIn)
  return (
    <ProviderAuthSection
      displayName="Kimi"
      authMode={authMode}
      onAuthModeChange={onAuthModeChange}
      authRegion={authRegion}
      onAuthRegionChange={onAuthRegionChange}
      flow={flow}
    />
  )
}
