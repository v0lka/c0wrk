import { create } from 'zustand'

interface PlanStep {
  id: string
  description: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number // milliseconds
}

interface EvalCriterion {
  id: string
  name: string
  description: string
  status: 'pass' | 'fail'
}

interface SessionStats {
  routingMode: string
  routingDomain: string
  routingComplexity: string
  attempt: number
  maxAttempts: number
}

interface InspectorState {
  // Data fields
  planSteps: PlanStep[]
  evalCriteria: EvalCriterion[]
  sessionStats: SessionStats

  // Actions
  setPlanSteps: (steps: PlanStep[]) => void
  updateStepStatus: (stepIndex: number, status: PlanStep['status']) => void
  updateStepById: (stepId: string, status: PlanStep['status'], duration?: number) => void
  setEvalCriteria: (criteria: EvalCriterion[]) => void
  updateStats: (update: Partial<SessionStats>) => void
  resetSessionData: () => void
}

const defaultStats: SessionStats = {
  routingMode: '',
  routingDomain: '',
  routingComplexity: '',
  attempt: 1,
  maxAttempts: 3,
}

export const useInspectorStore = create<InspectorState>((set) => ({
  planSteps: [],
  evalCriteria: [],
  sessionStats: { ...defaultStats },

  setPlanSteps: (steps) => set({ planSteps: steps }),
  updateStepStatus: (stepIndex, status) => set((s) => {
    const newSteps = [...s.planSteps]
    if (newSteps[stepIndex]) {
      newSteps[stepIndex] = { ...newSteps[stepIndex], status }
    }
    return { planSteps: newSteps }
  }),
  updateStepById: (stepId, status, duration) => set((s) => {
    const newSteps = [...s.planSteps]
    const index = newSteps.findIndex(step => step.id === stepId)
    if (index !== -1) {
      newSteps[index] = { ...newSteps[index], status, ...(duration !== undefined && { duration }) }
    }
    return { planSteps: newSteps }
  }),
  setEvalCriteria: (criteria) => set({ evalCriteria: criteria }),
  updateStats: (update) => set((s) => ({
    sessionStats: { ...s.sessionStats, ...update },
  })),
  resetSessionData: () => set({
    planSteps: [],
    evalCriteria: [],
    sessionStats: { ...defaultStats },
  }),
}))
