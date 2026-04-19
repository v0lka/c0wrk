// Hook for calling Wails Go bindings
// Wails v2 generates bindings at runtime accessible via window.go

import { useMemo } from 'react'
import type { SessionInfo, ProjectInfo, ChatMessage } from '@/lib/wails'
import type { desktop } from '../../wailsjs/go/models'

declare global {
  interface Window {
    go: {
      desktop: {
        App: {
          CreateSession(): Promise<SessionInfo>
          DeleteSession(id: string): Promise<void>
          ListSessions(): Promise<SessionInfo[]>
          RenameSession(id: string, name: string): Promise<void>
          ArchiveSession(id: string): Promise<void>
          SendMessage(id: string, text: string): Promise<void>
          CancelTask(id: string): Promise<void>
          GetSessionHistory(id: string): Promise<ChatMessage[]>
          GetConfig(): Promise<desktop.ConfigResponse>
          Greet(name: string): Promise<string>
          GetSecuritySettings?: () => Promise<Record<string, unknown>>
          UpdateSecuritySettings?: (settings: Record<string, unknown>) => Promise<void>
          GetSessionWorkspace(sessionID: string): Promise<string>
          ListDirectory(path: string): Promise<Array<{ name: string; path: string; is_dir: boolean }>>
          ListDirectoryRecursive(path: string): Promise<Array<{ name: string; path: string; is_dir: boolean }>>
          WatchDirectory(path: string): Promise<void>
          UnwatchDirectory(path: string): Promise<void>
          CreateProject(name: string, externalPath: string): Promise<ProjectInfo>
          DeleteProject(id: string): Promise<void>
          RenameProject(id: string, name: string): Promise<void>
          ListProjects(): Promise<ProjectInfo[]>
          SwitchProject(id: string): Promise<void>
          PickDirectory(): Promise<string>
          CheckCodebaseMemoryMCP(): Promise<[boolean, string]>
          InstallCodebaseMemoryMCP(): Promise<void>
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
  return useMemo(() => {
    if (typeof window === 'undefined') {
      return { api: undefined, runtime: undefined, isReady: false as const }
    }
    const api = window?.go?.desktop?.App
    const runtime = window?.runtime
    return { api, runtime, isReady: !!api && !!runtime }
  }, [])
}
