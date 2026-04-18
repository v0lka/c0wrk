import { create } from 'zustand'
import type { ProjectInfo } from '@/lib/wails'

// Helper to sort projects by last_active_at descending (most recent first).
// Used only for local mutations (addProject, updateProject).
// setProjects receives pre-sorted data from the backend and skips sorting.
function sortProjectsByActivity(projects: ProjectInfo[]): ProjectInfo[] {
  return [...projects].sort((a, b) => {
    const aTime = new Date(a.last_active_at || a.created_at).getTime()
    const bTime = new Date(b.last_active_at || b.created_at).getTime()
    return bTime - aTime
  })
}

interface ProjectState {
  projects: ProjectInfo[] | null  // null = not yet loaded, [] = loaded but empty
  activeProjectId: string | null
  setProjects: (projects: ProjectInfo[]) => void
  addProject: (project: ProjectInfo) => void
  removeProject: (id: string) => void
  setActiveProject: (id: string | null) => void
  updateProject: (id: string, updates: Partial<ProjectInfo>) => void
}

export const useProjectStore = create<ProjectState>((set) => ({
  projects: null,
  activeProjectId: null,
  setProjects: (projects) => set({ projects }),  // backend returns pre-sorted
  addProject: (project) => set((s) => {
    const current = s.projects ?? []
    const updated = [project, ...current]
    return { projects: sortProjectsByActivity(updated) }
  }),
  removeProject: (id) => set((s) => ({
    projects: (s.projects ?? []).filter(p => p.id !== id),
    activeProjectId: s.activeProjectId === id ? null : s.activeProjectId,
  })),
  setActiveProject: (id) => set({ activeProjectId: id }),
  updateProject: (id, updates) => set((s) => {
    const updated = (s.projects ?? []).map(p => p.id === id ? { ...p, ...updates } : p)
    return { projects: sortProjectsByActivity(updated) }
  }),
}))
