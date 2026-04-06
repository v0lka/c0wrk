import { useMemo } from 'react'
import { useWails } from './useWails'

export function useProjectAPI() {
  const { api } = useWails()

  return useMemo(() => ({
    createProject: (name: string, externalPath: string) => api?.CreateProject(name, externalPath),
    deleteProject: (id: string) => api?.DeleteProject(id),
    renameProject: (id: string, name: string) => api?.RenameProject(id, name),
    listProjects: () => api?.ListProjects(),
    switchProject: (id: string) => api?.SwitchProject(id),
    pickDirectory: () => api?.PickDirectory(),
  }), [api])
}
