// Project loader — handles initial project loading and project lifecycle events.

import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { listProjects } from '@/api/projects'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { useProjectStore } from '@/stores/projectStore'
import { isProjectInfo, isProjectRenamed } from '@/types/guards'

export function useProjectLoader(): void {
  const switchProjectWithState = useProjectSwitchState()
  useEffect(() => {
    let cancelled = false
    const cleanups: Array<() => void> = []
    const store = () => useProjectStore.getState()

    /** Fetch projects and auto-select the first one if none is active. */
    function loadAndActivate(): void {
      listProjects()
        .then((projects) => {
          if (cancelled) return
          store().setProjects(projects)
          if (!store().activeProjectId && projects.length > 0) {
            const firstId = projects[0]!.id
            switchProjectWithState(firstId).catch(() => { })
          }
        })
        .catch(() => { /* will retry on backend:ready */ })
    }

    // Subscribe to backend:ready (authoritative signal — backend is initialized)
    cleanups.push(subscribe('backend:ready', loadAndActivate))

    // Project lifecycle events
    cleanups.push(
      subscribe('project:created', (data: unknown) => {
        if (cancelled) return
        if (!isProjectInfo(data)) return
        store().addProject(data)
      }),
    )

    cleanups.push(
      subscribe('project:deleted', (data: unknown) => {
        if (cancelled) return
        if (typeof data !== 'string') return
        store().removeProject(data)
      }),
    )

    cleanups.push(
      subscribe('project:renamed', (data: unknown) => {
        if (cancelled) return
        if (!isProjectRenamed(data)) return
        store().updateProject(data.id, { name: data.name })
      }),
    )

    cleanups.push(
      subscribe('project:switched', (data: unknown) => {
        if (cancelled) return
        if (!isProjectInfo(data)) return
        store().setActiveProjectId(data.id)
      }),
    )

    // Attempt initial load immediately (backend may already be ready)
    loadAndActivate()

    return () => {
      cancelled = true
      cleanups.forEach(fn => fn())
    }
  }, [switchProjectWithState])
}
