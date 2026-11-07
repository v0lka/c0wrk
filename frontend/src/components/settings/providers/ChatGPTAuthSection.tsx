import { useChatGPTAuthFlow } from '@/hooks/useChatGPTAuthFlow'
import type { ChatGPTAuthMode } from '@/types/models'
import { ProviderAuthSection } from './ProviderAuthSection'

interface ChatGPTAuthSectionProps {
  authMode: ChatGPTAuthMode
  onAuthModeChange: (mode: ChatGPTAuthMode) => void
}

export function ChatGPTAuthSection({ authMode, onAuthModeChange }: ChatGPTAuthSectionProps) {
  const flow = useChatGPTAuthFlow()
  return (
    <ProviderAuthSection
      displayName="ChatGPT"
      authMode={authMode}
      onAuthModeChange={onAuthModeChange}
      flow={flow}
    />
  )
}
