import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface PlanReviewState {
  planReview: boolean
  setPlanReview: (planReview: boolean) => void
}

export const usePlanReviewStore = create<PlanReviewState>()(
  persist(
    (set) => ({
      planReview: false,
      setPlanReview: (planReview) => set({ planReview }),
    }),
    {
      name: 'c0wrk-plan-review',
      version: 1,
      migrate: (persistedState, _version) => persistedState,
    }
  )
)
