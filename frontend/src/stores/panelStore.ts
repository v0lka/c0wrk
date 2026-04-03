import { create } from 'zustand'
import { useShallow } from 'zustand/shallow'
import type { ChatMessageUI } from './chatStore'

// Types matching backend event data structures
export interface PlanItem {
  id: string        // step_id from backend (1-indexed string)
  title: string     // step description
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number // milliseconds, from plan_step_complete
}

export interface EvalItem {
  name: string        // criterion name/ID
  description: string // criterion description
  status: 'pending' | 'pass' | 'fail' | 'unclear'
}

export interface PlanGroup {
  id: number
  items: PlanItem[]
}

export interface EvalGroup {
  id: number
  items: EvalItem[]
}

interface PanelState {
  planGroups: PlanGroup[]  // newest first
  evalGroups: EvalGroup[]  // newest first
  
  // Actions
  addPlanGroup: (steps: Array<{ description: string; status?: string }>) => void
  updatePlanItemStatus: (stepId: string, status: PlanItem['status'], duration?: number) => void
  addEvalGroup: (criteria: Array<{ name: string; description?: string; passed?: boolean }>) => void
  updateEvalGroupStatuses: (criteria: Array<{ name: string; description?: string; passed?: boolean }>) => void
  resetPanels: () => void
  resetEvalStatuses: () => void
  resetPlanStatuses: () => void
  rebuildFromEvents: (messages: ChatMessageUI[]) => void
}

let planGroupCounter = 0
let evalGroupCounter = 0

export const usePanelStore = create<PanelState>((set) => ({
  planGroups: [],
  evalGroups: [],

  addPlanGroup: (steps) => {
    const newGroup: PlanGroup = {
      id: ++planGroupCounter,
      items: steps.map((s, i) => ({
        id: (s as { id?: string }).id || String(i + 1),
        title: s.description,
        status: (s.status as PlanItem['status']) || 'pending',
      })),
    }
    set((state) => ({
      planGroups: [newGroup, ...state.planGroups],
    }))
  },

  updatePlanItemStatus: (stepId, status, duration) => {
    set((state) => {
      if (state.planGroups.length === 0) return state
      
      // Update the latest (first) plan group
      const [latestGroup, ...rest] = state.planGroups
      const updatedItems = latestGroup.items.map((item) =>
        item.id === stepId
          ? { ...item, status, ...(duration !== undefined ? { duration } : {}) }
          : item
      )
      
      return {
        planGroups: [{ ...latestGroup, items: updatedItems }, ...rest],
      }
    })
  },

  addEvalGroup: (criteria) => {
    const newGroup: EvalGroup = {
      id: ++evalGroupCounter,
      items: criteria.map((c) => ({
        name: c.name,
        description: c.description || c.name,
        status: c.passed === undefined ? 'pending' as const : c.passed ? 'pass' as const : 'fail' as const,
      })),
    }
    set((state) => ({
      evalGroups: [newGroup, ...state.evalGroups],
    }))
  },

  updateEvalGroupStatuses: (criteria) => {
    set((state) => {
      if (state.evalGroups.length === 0) {
        // No existing group, create one
        const newGroup: EvalGroup = {
          id: ++evalGroupCounter,
          items: criteria.map((c) => ({
            name: c.name,
            description: c.description || c.name,
            status: c.passed === undefined ? 'pending' as const : c.passed ? 'pass' as const : 'fail' as const,
          })),
        }
        return { evalGroups: [newGroup] }
      }
      // Update the latest group (index 0)
      const [latest, ...rest] = state.evalGroups
      const updatedItems = latest.items.map((item) => {
        const match = criteria.find((c) => c.name === item.name)
        if (match) {
          return {
            ...item,
            status: match.passed === undefined ? 'pending' as const : match.passed ? 'pass' as const : 'fail' as const,
          }
        }
        return item
      })
      return {
        evalGroups: [{ ...latest, items: updatedItems }, ...rest],
      }
    })
  },

  resetPanels: () => {
    planGroupCounter = 0
    evalGroupCounter = 0
    set({ planGroups: [], evalGroups: [] })
  },

  resetEvalStatuses: () => {
    set((state) => {
      if (state.evalGroups.length === 0) return state
      const [latest, ...rest] = state.evalGroups
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
      const [latest, ...rest] = state.planGroups
      return {
        planGroups: [{
          ...latest,
          items: latest.items.map(item => ({ ...item, status: 'pending' as const, duration: undefined })),
        }, ...rest],
      }
    })
  },

  rebuildFromEvents: (messages) => {
    // Reset counters
    planGroupCounter = 0
    evalGroupCounter = 0
    
    const planGroups: PlanGroup[] = []
    const evalGroups: EvalGroup[] = []
    
    // Iterate chronologically to rebuild state
    for (const msg of messages) {
      switch (msg.type) {
        case 'plan': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const rawSteps = (meta?.steps as Array<{ id?: string; description: string; status?: string }>) || []
          const group: PlanGroup = {
            id: ++planGroupCounter,
            items: rawSteps.map((s, i) => ({
              id: s.id || String(i + 1),
              title: s.description,
              status: (s.status as PlanItem['status']) || 'pending',
            })),
          }
          planGroups.push(group)
          break
        }

        case 'plan_step_start': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const stepId = meta?.step_id as string | undefined
          if (planGroups.length > 0 && stepId) {
            const latestGroup = planGroups[planGroups.length - 1]
            const step = latestGroup.items.find((s) => s.id === stepId)
            if (step) step.status = 'running'
          }
          break
        }

        case 'plan_step_complete': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const stepId = meta?.step_id as string | undefined
          const success = meta?.success as boolean | undefined
          const duration = meta?.duration as number | undefined
          if (planGroups.length > 0 && stepId) {
            const latestGroup = planGroups[planGroups.length - 1]
            const step = latestGroup.items.find((s) => s.id === stepId)
            if (step) {
              step.status = success ? 'completed' : 'failed'
              if (duration !== undefined) step.duration = duration
            }
          }
          break
        }

        case 'eval': {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const rawCriteria = (meta?.criteria as Array<{ name: string; description?: string; passed: boolean }>) || []
          // Check if latest group has matching criteria names - update instead of creating new
          if (evalGroups.length > 0) {
            const latestGroup = evalGroups[evalGroups.length - 1]
            const latestNames = new Set(latestGroup.items.map((i) => i.name))
            const incomingNames = new Set(rawCriteria.map((c) => c.name))
            const isMatch = rawCriteria.length > 0 && 
              rawCriteria.every((c) => latestNames.has(c.name)) &&
              latestGroup.items.every((i) => incomingNames.has(i.name))
            if (isMatch) {
              // Update existing group's statuses
              latestGroup.items = latestGroup.items.map((item) => {
                const match = rawCriteria.find((c) => c.name === item.name)
                if (match) {
                  return {
                    ...item,
                    status: match.passed ? 'pass' as const : 'fail' as const,
                  }
                }
                return item
              })
              break
            }
          }
          // No matching group found, create new one
          const group: EvalGroup = {
            id: ++evalGroupCounter,
            items: rawCriteria.map((c) => ({
              name: c.name,
              description: c.description || c.name,
              status: c.passed ? 'pass' as const : 'fail' as const,
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
    })
  },
}))

// Computed selectors with equality comparators for performance
// Using useShallow for computed values to prevent unnecessary re-renders
export function usePlanCompleted(): number {
  return usePanelStore(
    useShallow((state) => {
      let count = 0
      for (const group of state.planGroups) {
        for (const item of group.items) {
          if (item.status === 'completed' || item.status === 'failed') {
            count++
          }
        }
      }
      return count
    })
  )
}

export function usePlanTotal(): number {
  return usePanelStore(
    useShallow((state) => {
      let count = 0
      for (const group of state.planGroups) {
        count += group.items.length
      }
      return count
    })
  )
}

export function useEvalCompleted(): number {
  return usePanelStore(
    useShallow((state) => {
      let count = 0
      for (const group of state.evalGroups) {
        for (const item of group.items) {
          if (item.status === 'pass' || item.status === 'fail' || item.status === 'unclear') {
            count++
          }
        }
      }
      return count
    })
  )
}

export function useEvalTotal(): number {
  return usePanelStore(
    useShallow((state) => {
      let count = 0
      for (const group of state.evalGroups) {
        count += group.items.length
      }
      return count
    })
  )
}
