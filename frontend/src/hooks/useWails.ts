// Hook for calling Wails Go bindings
// Wails v2 generates bindings at runtime accessible via window.go

import { useMemo } from 'react'
import type { SessionInfo, ProjectInfo, ChatMessage } from '@/lib/wails'
import type { main } from '../../wailsjs/go/models'

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
          GetConfig(): Promise<main.ConfigResponse>
          Greet(name: string): Promise<string>
          GetSecuritySettings?: () => Promise<Record<string, unknown>>
          UpdateSecuritySettings?: (settings: Record<string, unknown>) => Promise<void>
          GetSessionWorkspace(sessionID: string): Promise<string>
          ListDirectory(path: string): Promise<Array<{ name: string; path: string; is_dir: boolean }>>
          WatchDirectory(path: string): Promise<void>
          UnwatchDirectory(path: string): Promise<void>
          CreateProject(name: string, externalPath: string): Promise<ProjectInfo>
          DeleteProject(id: string): Promise<void>
          RenameProject(id: string, name: string): Promise<void>
          ListProjects(): Promise<ProjectInfo[]>
          SwitchProject(id: string): Promise<void>
          PickDirectory(): Promise<string>
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
    isReady: !!api && !!runtime,
  }), [api, runtime])
}
