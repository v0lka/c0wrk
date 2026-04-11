import { useMemo } from 'react'
import { useWails } from './useWails'
import { useProjectStore } from '@/stores/projectStore'

export function useSessionAPI() {
  const { api } = useWails()

  return useMemo(() => ({
    createSession: () => api?.CreateSession(),
    deleteSession: (id: string) => api?.DeleteSession(id),
    listSessions: () => api?.ListSessions(),
    renameSession: (id: string, name: string) => api?.RenameSession(id, name),
    archiveSession: (id: string) => api?.ArchiveSession(id),
    sendMessage: (id: string, text: string, planFirst: boolean) => api?.SendMessage(id, text, planFirst),
    cancelTask: (id: string) => api?.CancelTask(id),
    getHistory: (id: string) => api?.GetSessionHistory(id),
    getConfig: () => api?.GetConfig(),
  }), [api])
}

/** Returns true when it's safe to create a new session in the active project. */
export function useCanCreateSession(): boolean {
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  return !!activeProjectId
}
