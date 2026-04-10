import { create } from 'zustand'
import type { ChatMessageUI } from './chatStore'

// Types matching backend event data structures
export interface PlanItem {
  id: string        // step_id from backend (1-indexed string)
  title: string     // step description
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number // milliseconds, from plan_step_complete
  dependsOn: string[] // DAG dependency IDs
}

export interface EvalItem {
  name: string        // criterion name/ID
  description: string // criterion description
  status: 'pending' | 'pass' | 'fail' | 'unclear'
  diagnostic?: string
}

export interface PlanGroup {
  id: number
  items: PlanItem[]
  // Backend-computed progress fields (when available)
  progress?: number          // 0.0–1.0
  completedCount?: number
  totalCount?: number
}

export interface EvalGroup {
  id: number
  items: EvalItem[]
}

interface SessionStats {
  routingDomain: string
  routingComplexity: string
  attempt: number
  maxAttempts: number
}

const defaultStats: SessionStats = {
  routingDomain: '',
  routingComplexity: '',
  attempt: 1,
  maxAttempts: 3,
}

interface PanelState {
  planGroups: PlanGroup[]  // newest first
  evalGroups: EvalGroup[]  // newest first
  sessionStats: SessionStats
  _planGroupCounter: number
  _evalGroupCounter: number
  
  // Actions
  addPlanGroup: (steps: Array<{ description: string; status?: string }>, progress?: { progress?: number; completed_count?: number; total_count?: number }) => void
  updatePlanItemStatus: (stepId: string, status: PlanItem['status'], duration?: number) => void
  addEvalGroup: (criteria: Array<{ name: string; description?: string; passed?: boolean; status?: 'pass' | 'fail' | 'unclear'; diagnostic?: string }>) => void
  updateEvalGroupStatuses: (criteria: Array<{ name: string; description?: string; passed?: boolean; status?: 'pass' | 'fail' | 'unclear'; diagnostic?: string }>) => void
  updateStats: (update: Partial<SessionStats>) => void
  resetPanels: () => void
  resetEvalStatuses: () => void
  resetPlanStatuses: () => void
  rebuildFromEvents: (messages: ChatMessageUI[]) => void
}

const validPlanStatuses = ['pending', 'running', 'completed', 'failed'] as const
type ValidPlanStatus = typeof validPlanStatuses[number]
function toValidStatus(s: unknown): ValidPlanStatus {
  return typeof s === 'string' && (validPlanStatuses as readonly string[]).includes(s)
    ? (s as ValidPlanStatus) : 'pending'
}

export const usePanelStore = create<PanelState>((set) => ({
  planGroups: [],
  evalGroups: [],
  sessionStats: { ...defaultStats },
  _planGroupCounter: 0,
  _evalGroupCounter: 0,

  addPlanGroup: (steps, progress) => {
    if (!steps) return  // Guard against null/undefined from backend nil slices
    set((state) => {
      const counter = state._planGroupCounter + 1
      const newGroup: PlanGroup = {
        id: counter,
        items: steps.map((s, i) => ({
          id: (s as { id?: string }).id || String(i + 1),
          title: s.description,
          status: toValidStatus(s.status),
          dependsOn: (s as { depends_on?: string[] }).depends_on || [],
        })),
        progress: progress?.progress,
        completedCount: progress?.completed_count,
        totalCount: progress?.total_count,
      }
      return {
        _planGroupCounter: counter,
        planGroups: [newGroup, ...state.planGroups],
      }
    })
  },

  updatePlanItemStatus: (stepId, status, duration) => {
    set((state) => {
      if (state.planGroups.length === 0) return state
      
      // Update the latest (first) plan group
      const latestGroup = state.planGroups[0]! // Safe: length > 0 checked above
      const rest = state.planGroups.slice(1)
      const updatedItems = latestGroup.items.map((item) =>
        item.id === stepId
          ? { ...item, status, ...(duration !== undefined ? { duration } : {}) }
          : item
      )

      // Recompute completedCount from items to keep group-level counter in sync
      const completedCount = updatedItems.filter(
        (item) => item.status === 'completed' || item.status === 'failed'
      ).length

      return {
        planGroups: [{
          ...latestGroup,
          items: updatedItems,
          completedCount,
        }, ...rest],
      }
    })
  },

  addEvalGroup: (criteria) => {
    if (!criteria) return  // Guard against null/undefined from backend nil slices
    set((state) => {
      const counter = state._evalGroupCounter + 1
      const newGroup: EvalGroup = {
        id: counter,
        items: criteria.map((c) => ({
          name: c.name,
          description: c.description || c.name,
          status: c.status ?? (c.passed === undefined ? 'pending' as const : c.passed ? 'pass' as const : 'fail' as const),
          ...(c.diagnostic !== undefined ? { diagnostic: c.diagnostic } : {}),
        })),
      }
      return {
        _evalGroupCounter: counter,
        evalGroups: [newGroup, ...state.evalGroups],
      }
    })
  },

  updateEvalGroupStatuses: (criteria) => {
    if (!criteria) return  // Guard against null/undefined from backend nil slices
    set((state) => {
      if (state.evalGroups.length === 0) {
        // No existing group, create one
        const counter = state._evalGroupCounter + 1
        const newGroup: EvalGroup = {
          id: counter,
          items: criteria.map((c) => ({
            name: c.name,
            description: c.description || c.name,
            status: c.status ?? (c.passed === undefined ? 'pending' as const : c.passed ? 'pass' as const : 'fail' as const),
            ...(c.diagnostic !== undefined ? { diagnostic: c.diagnostic } : {}),
          })),
        }
        return { _evalGroupCounter: counter, evalGroups: [newGroup] }
      }
      // Update the latest group (index 0)
      const latest = state.evalGroups[0]! // Safe: length > 0 checked above
      const rest = state.evalGroups.slice(1)
      const updatedItems = latest.items.map((item) => {
        const match = criteria.find((c) => c.name === item.name)
        if (match) {
          return {
            ...item,
            status: match.status ?? (match.passed === undefined ? 'pending' as const : match.passed ? 'pass' as const : 'fail' as const),
            ...(match.diagnostic !== undefined ? { diagnostic: match.diagnostic } : {}),
          }
        }
        return item
      })
      return {
        evalGroups: [{ ...latest, items: updatedItems }, ...rest],
      }
    })
  },

  updateStats: (update) => set((s) => ({
    sessionStats: { ...s.sessionStats, ...update },
  })),

  resetPanels: () => {
    set({ planGroups: [], evalGroups: [], sessionStats: { ...defaultStats }, _planGroupCounter: 0, _evalGroupCounter: 0 })
  },

  resetEvalStatuses: () => {
    set((state) => {
      if (state.evalGroups.length === 0) return state
      const latest = state.evalGroups[0]! // Safe: length > 0 checked above
      const rest = state.evalGroups.slice(1)
      return {
        evalGroups: [{
          ...latest,
          items: latest.items.map(item => ({ ...item, status: 'pending' as const })),
        }, ...rest],
      }
    })
  },

  resetPlanStatuses: () => {
    set((state) => {
      if (state.planGroups.length === 0) return state
      const latest = state.planGroups[0]! // Safe: length > 0 checked above
      const rest = state.planGroups.slice(1)
      return {
        planGroups: [{
          ...latest,
          items: latest.items.map(item => ({ ...item, status: 'pending' as const, duration: undefined })),
        }, ...rest],
      }
    })
  },

  rebuildFromEvents: (messages) => {
    let planCounter = 0
    let evalCounter = 0
    
    let planGroups: PlanGroup[] = []
    let evalGroups: EvalGroup[] = []
    
    // Iterate chronologically to rebuild state
    for (const msg of messages) {
      switch (msg.type) {
        case 'plan': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const rawSteps = (meta?.steps as Array<{ id?: string; description: string; status?: string }>) || []
          const group: PlanGroup = {
            id: ++planCounter,
            items: rawSteps.map((s, i) => ({
              id: s.id || String(i + 1),
              title: s.description,
              status: toValidStatus(s.status),
              dependsOn: (s as { depends_on?: string[] }).depends_on || [],
            })),
          }
          planGroups.push(group)
          break
        }

        case 'plan_step_start': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const stepId = meta?.step_id as string | undefined
          if (planGroups.length > 0 && stepId) {
            const latest = planGroups[planGroups.length - 1]! // Safe: length > 0 checked
            const updatedItems = latest.items.map((s) =>
              s.id === stepId ? { ...s, status: 'running' as const } : s
            )
            planGroups = planGroups.map((g, i) =>
              i === planGroups.length - 1 ? { ...latest, items: updatedItems } : g
            )
          }
          break
        }

        case 'plan_step_complete': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const stepId = meta?.step_id as string | undefined
          const success = meta?.success as boolean | undefined
          const duration = meta?.duration as number | undefined
          if (planGroups.length > 0 && stepId) {
            const latest = planGroups[planGroups.length - 1]! // Safe: length > 0 checked
            const updatedItems = latest.items.map((s) =>
              s.id === stepId
                ? {
                    ...s,
                    status: (success ? 'completed' : 'failed') as PlanItem['status'],
                    ...(duration !== undefined ? { duration } : {}),
                  }
                : s
            )
            planGroups = planGroups.map((g, i) =>
              i === planGroups.length - 1 ? { ...latest, items: updatedItems } : g
            )
          }
          break
        }

        case 'eval': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const rawCriteria = (meta?.criteria as Array<{ name: string; description?: string; passed: boolean; status?: 'pass' | 'fail' | 'unclear'; diagnostic?: string }>) || []
          // Check if latest group has matching criteria names - update instead of creating new
          if (evalGroups.length > 0) {
            const latestGroup = evalGroups[evalGroups.length - 1]! // Safe: length > 0 checked
            const latestNames = new Set(latestGroup.items.map((i) => i.name))
            const incomingNames = new Set(rawCriteria.map((c) => c.name))
            const isMatch = rawCriteria.length > 0 && 
              rawCriteria.every((c) => latestNames.has(c.name)) &&
              latestGroup.items.every((i) => incomingNames.has(i.name))
            if (isMatch) {
              // Update existing group's statuses immutably
              const updatedItems = latestGroup.items.map((item) => {
                const match = rawCriteria.find((c) => c.name === item.name)
                if (match) {
                  return {
                    ...item,
                    status: match.status ?? (match.passed ? 'pass' as const : 'fail' as const),
                    ...(match.diagnostic !== undefined ? { diagnostic: match.diagnostic } : {}),
                  }
                }
                return item
              })
              evalGroups = evalGroups.map((g, i) =>
                i === evalGroups.length - 1 ? { ...latestGroup, items: updatedItems } : g
              )
              break
            }
          }
          // No matching group found, create new one
          const group: EvalGroup = {
            id: ++evalCounter,
            items: rawCriteria.map((c) => ({
              name: c.name,
              description: c.description || c.name,
              status: c.status ?? (c.passed ? 'pass' as const : 'fail' as const),
              ...(c.diagnostic !== undefined ? { diagnostic: c.diagnostic } : {}),
            })),
          }
          evalGroups.push(group)
          break
        }

        default:
          break
      }
    }
    
    // Store newest first
    set({
      planGroups: planGroups.reverse(),
      evalGroups: evalGroups.reverse(),
      _planGroupCounter: planCounter,
      _evalGroupCounter: evalCounter,
    })
  },
}))

// Stable module-level selectors for computed values.
// These return primitive numbers so Zustand's default Object.is comparison
// is sufficient — no useShallow needed. Keeping selector references stable
// ensures useSyncExternalStore reliably detects store changes and triggers
// re-renders (fixes collapsed panel header counter not updating).

const selectPlanCompleted = (state: PanelState): number => {
  // Use backend-provided count from latest group if available
  const latestPlanForCompleted = state.planGroups[0]
  if (latestPlanForCompleted?.completedCount !== undefined) {
    return latestPlanForCompleted.completedCount
  }
  // Fallback: compute from items
  let count = 0
  for (const group of state.planGroups) {
    for (const item of group.items) {
      if (item.status === 'completed' || item.status === 'failed') {
        count++
      }
    }
  }
  return count
}

const selectPlanTotal = (state: PanelState): number => {
  // Use backend-provided count from latest group if available
  const latestPlanForTotal = state.planGroups[0]
  if (latestPlanForTotal?.totalCount !== undefined) {
    return latestPlanForTotal.totalCount
  }
  // Fallback: compute from items
  let count = 0
  for (const group of state.planGroups) {
    count += group.items.length
  }
  return count
}

const selectEvalCompleted = (state: PanelState): number => {
  let count = 0
  for (const group of state.evalGroups) {
    for (const item of group.items) {
      if (item.status === 'pass' || item.status === 'fail' || item.status === 'unclear') {
        count++
      }
    }
  }
  return count
}

const selectEvalTotal = (state: PanelState): number => {
  let count = 0
  for (const group of state.evalGroups) {
    count += group.items.length
  }
  return count
}

export function usePlanCompleted(): number {
  return usePanelStore(selectPlanCompleted)
}

export function usePlanTotal(): number {
  return usePanelStore(selectPlanTotal)
}

export function useEvalCompleted(): number {
  return usePanelStore(selectEvalCompleted)
}

export function useEvalTotal(): number {
  return usePanelStore(selectEvalTotal)
}
