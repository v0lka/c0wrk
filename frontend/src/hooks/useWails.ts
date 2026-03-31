// Hook for calling Wails Go bindings
// Wails v2 generates bindings at runtime accessible via window.go

import { useMemo } from 'react'
import type { SessionInfo, ChatMessage } from '@/lib/wails'

declare global {
  interface Window {
    go: {
      main: {
        App: {
          CreateSession(): Promise<SessionInfo>
          DeleteSession(id: string): Promise<void>
          ListSessions(): Promise<SessionInfo[]>
          RenameSession(id: string, name: string): Promise<void>
          ArchiveSession(id: string): Promise<void>
          SendMessage(id: string, text: string): Promise<void>
          CancelTask(id: string): Promise<void>
          GetSessionHistory(id: string): Promise<ChatMessage[]>
          GetConfig(): Promise<Record<string, unknown>>
          Greet(name: string): Promise<string>
          GetSecuritySettings?: () => Promise<Record<string, unknown>>
          UpdateSecuritySettings?: (settings: Record<string, unknown>) => Promise<void>
        }
      }
    }
    runtime: {
      EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void
      EventsEmit(eventName: string, ...data: unknown[]): void
    }
  }
}

export function useWails() {
  const api = typeof window !== 'undefined' ? window?.go?.main?.App : undefined
  const runtime = typeof window !== 'undefined' ? window?.runtime : undefined

  return useMemo(() => ({
    api,
    runtime,
    isReady: !!api,
  }), [api, runtime])
}
