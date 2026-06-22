// Project loader — handles initial project loading and project lifecycle events.

import { useEffect, useRef } from 'react'
import { subscribe } from '@/api/runtime'
import { listProjects } from '@/api/projects'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { useProjectStore } from '@/stores/projectStore'
import { isProjectInfo, isProjectRenamed } from '@/types/guards'

export function useProjectLoader(): void {
  const switchProjectWithState = useProjectSwitchState()
  // Prevent concurrent loadAndActivate calls (immediate mount + backend:ready race).
  const loadingRef = useRef(false)
  // Queue a retry when a backend:ready event arrives while a load is in-flight.
  const needsRetryRef = useRef(false)

  useEffect(() => {
    let cancelled = false
    const cleanups: Array<() => void> = []
    const store = () => useProjectStore.getState()

    /** Fetch projects and auto-select the first one if none is active. */
    async function loadAndActivate(): Promise<void> {
      if (loadingRef.current) {
        needsRetryRef.current = true
        return
      }
      loadingRef.current = true
      needsRetryRef.current = false
      try {
        const projects = await listProjects()
        if (cancelled) return
        store().setProjects(projects)
        if (!store().activeProjectId && projects.length > 0) {
          const firstId = projects[0]!.id
          await switchProjectWithState(firstId).catch(() => { })
        }
      } catch {
        /* will retry on next backend:ready */
      } finally {
        loadingRef.current = false
        if (needsRetryRef.current) {
          needsRetryRef.current = false
          loadAndActivate()
        }
      }
    }

    // Subscribe to backend:ready (authoritative signal — backend is initialized).
    // Also handles re-emission after LLM config is saved.
    cleanups.push(subscribe('backend:ready', () => { loadAndActivate() }))

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
