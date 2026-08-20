import { useChatStore } from '@/stores/chatStore'

/** Apply the authoritative backend transition when a cooperative pause lands. */
export function handleSessionPausedEvent(sessionId: string): void {
  const store = useChatStore.getState()
  store.setPausing(sessionId, false)
  store.setPaused(sessionId, true)
  store.setTaskActive(sessionId, false)
  store.setActivityStatus(sessionId, 'Paused')
}

/** Apply the authoritative backend transition when a paused session resumes. */
export function handleSessionResumedEvent(sessionId: string): void {
  const store = useChatStore.getState()
  store.setPausing(sessionId, false)
  store.setPaused(sessionId, false)
  store.setTaskActive(sessionId, true)
  store.setActivityStatus(sessionId, 'Resuming...')
}
