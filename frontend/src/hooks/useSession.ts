import { useMemo } from 'react'
import { useWails } from './useWails'

export function useSessionAPI() {
  const { api } = useWails()

  return useMemo(() => ({
    createSession: () => api?.CreateSession(),
    deleteSession: (id: string) => api?.DeleteSession(id),
    listSessions: () => api?.ListSessions(),
    renameSession: (id: string, name: string) => api?.RenameSession(id, name),
    archiveSession: (id: string) => api?.ArchiveSession(id),
    sendMessage: (id: string, text: string) => api?.SendMessage(id, text),
    cancelTask: (id: string) => api?.CancelTask(id),
    getHistory: (id: string) => api?.GetSessionHistory(id),
    getConfig: () => api?.GetConfig(),
  }), [api])
}
