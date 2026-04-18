import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { usePanelStore } from '@/stores/panelStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useWails } from './useWails'
import type { RoutingData, ToolCallData, ToolResultData, PlanData, ToolConfirmData, ThoughtData, PlanStepStartData, PlanStepCompleteData, ContextFillData, AskUserData, AssistantChunkData, StepLimitData } from '@/lib/wails'
import { isSessionTokensData, isContextCompactionData } from '@/lib/wails'
import { GetSessionTokens } from '../../wailsjs/go/desktop/App'

// --- Type guards for event data validation ---
function isRoutingData(data: unknown): data is RoutingData {
  return typeof data === 'object' && data !== null && 'domain' in data && 'complexity' in data
}

function isStepData(data: unknown): data is { step_num: number } {
  return typeof data === 'object' && data !== null && 'step_num' in data
}

function isThoughtData(data: unknown): data is ThoughtData {
  return typeof data === 'object' && data !== null && 'content' in data && 'step_num' in data
}

function isToolCallData(data: unknown): data is ToolCallData {
  return typeof data === 'object' && data !== null && 'tool' in data && 'step' in data
}

function isToolResultData(data: unknown): data is ToolResultData {
  return typeof data === 'object' && data !== null && 'step' in data && 'result_len' in data
}

function isToolConfirmData(data: unknown): data is ToolConfirmData {
  return typeof data === 'object' && data !== null && 'confirm_id' in data && 'tool' in data
}

function isAskUserData(data: unknown): data is AskUserData {
  return typeof data === 'object' && data !== null && 'request_id' in data && 'questions' in data
}

function isStepLimitData(data: unknown): data is StepLimitData {
  return typeof data === 'object' && data !== null && 'request_id' in data && 'current_step' in data && 'max_steps' in data
}

function isPlanData(data: unknown): data is PlanData {
  return typeof data === 'object' && data !== null && 'step_count' in data
}

function isPlanStepStartData(data: unknown): data is PlanStepStartData {
  return typeof data === 'object' && data !== null && 'step_id' in data
}

function isPlanStepCompleteData(data: unknown): data is PlanStepCompleteData {
  return typeof data === 'object' && data !== null && 'step_id' in data && 'success' in data
}

function isAssistantChunkData(data: unknown): data is AssistantChunkData {
  return typeof data === 'object' && data !== null && ('content' in data || 'accumulated_content' in data)
}

function isErrorData(data: unknown): data is { error: string } {
  return typeof data === 'object' && data !== null && 'error' in data
}

function isTaskCompleteData(data: unknown): data is { output?: string; attempt_count?: number; routing_decision?: Record<string, unknown> } {
  return typeof data === 'object' && data !== null && ('output' in data || 'attempt_count' in data || 'routing_decision' in data)
}

function isRetryData(data: unknown): data is { attempt: number; max_attempts: number } {
  return typeof data === 'object' && data !== null && 'attempt' in data && 'max_attempts' in data
}

function isStepRetryData(data: unknown): data is { step_id: string; attempt: number; max_attempts: number } {
  return typeof data === 'object' && data !== null && 'step_id' in data && 'attempt' in data && 'max_attempts' in data
}

function isServiceData(data: unknown): data is { content: string } {
  return typeof data === 'object' && data !== null && 'content' in data
}

function isSubAgentLaunchData(data: unknown): data is { step_id: string; description: string; plan_step_id?: string } {
  return typeof data === 'object' && data !== null && 'step_id' in data
}

function isSubAgentCompleteData(data: unknown): data is { step_id: string; success: boolean; duration: number; plan_step_id?: string } {
  return typeof data === 'object' && data !== null && 'step_id' in data && 'success' in data
}

function isContextFillData(data: unknown): data is ContextFillData {
  return typeof data === 'object' && data !== null && 'fill_percent' in data && 'status' in data
}

function isSessionRenamedData(data: unknown): data is { new_name: string } {
  return typeof data === 'object' && data !== null && 'new_name' in data
}

export function useSessionEvents(sessionId: string | null) {
  const { runtime } = useWails()

  useEffect(() => {
    if (!sessionId || !runtime) return
    let mounted = true

    const panelStore = usePanelStore.getState()

    // Helper: only update global UI state if this session is still active
    const isActiveSession = () => useSessionStore.getState().activeSessionId === sessionId

    // Reset global UI state from previous session
    useChatStore.getState().clearSessionUIState()

    // Load persisted session token totals
    GetSessionTokens(sessionId).then((resp) => {
      if (!mounted || !isActiveSession()) return
      useChatStore.getState().setSessionTokens(resp.total_input_tokens ?? 0, resp.total_output_tokens ?? 0)
    }).catch(() => { }) // Ignore errors, will show 0

    // Reset panel data for new session (before any events arrive)
    panelStore.resetPanels()

    const unsubs: (() => void)[] = []
    const on = (type: string, cb: (...data: unknown[]) => void) => {
      unsubs.push(runtime.EventsOn(`session:${sessionId}:${type}`, cb))
    }

    on('routing', (data: unknown) => {
      if (!mounted) return
      if (!isRoutingData(data)) return
      const routing = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Analyzing request...')
      useChatStore.getState().addMessage(sessionId, {
        id: `routing-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Domain: ${routing.domain} | Complexity: ${routing.complexity}`,
        metadata: { domain: routing.domain, complexity: routing.complexity },
        timestamp: Date.now(),
      })
      panelStore.updateStats({ routingDomain: routing.domain, routingComplexity: routing.complexity })
    })

    on('step_start', (data: unknown) => {
      if (!mounted) return
      if (!isStepData(data)) return
      if (isActiveSession()) {
        useChatStore.getState().setThinking(true)
        useChatStore.getState().setActivityStatus('Thinking...')
      }
      const step = data
      useChatStore.getState().addMessage(sessionId, {
        id: `step-${step.step_num}`,
        sessionId,
        type: 'thinking',
        content: `Step ${step.step_num || ''}...`,
        timestamp: Date.now(),
      })
    })

    on('thought', (data: unknown) => {
      if (!mounted) return
      if (!isThoughtData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reasoning...')
      const thought = data
      useChatStore.getState().addMessage(sessionId, {
        id: `thought-${thought.step_num}-${Date.now()}`,
        sessionId,
        type: 'thought',
        content: thought.content,
        metadata: { step_num: thought.step_num, reasoning: thought.reasoning, plan_step_id: thought.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('tool_call', (data: unknown) => {
      if (!mounted) return
      if (!isToolCallData(data)) return
      const toolCall = data
      const isMemoryTool = ['read_evidence', 'read_step_output', 'list_step_outputs', 'store_fact', 'search_facts'].includes(toolCall.tool)
      if (isActiveSession()) useChatStore.getState().setActivityStatus(isMemoryTool ? 'Using memory...' : `Running tool: ${toolCall.tool}...`)
      const callIdx = toolCall.call_idx
      const retryAttempt = toolCall.retry_attempt
      const retrySuffix = retryAttempt ? `-r${retryAttempt}` : ''
      const toolMsgId = toolCall.plan_step_id
        ? `tool-${toolCall.plan_step_id}-${toolCall.step}${callIdx !== undefined ? `-${callIdx}` : ''}${retrySuffix}`
        : `tool-${toolCall.step}${callIdx !== undefined ? `-${callIdx}` : ''}${retrySuffix}`
      useChatStore.getState().addMessage(sessionId, {
        id: toolMsgId,
        sessionId,
        type: 'tool_call',
        content: `${toolCall.tool}(${toolCall.args})`,
        metadata: { step: toolCall.step, tool: toolCall.tool, args: toolCall.args, parsed_args: toolCall.parsed_args, plan_step_id: toolCall.plan_step_id, source: toolCall.source, call_idx: toolCall.call_idx, retry_attempt: toolCall.retry_attempt },
        timestamp: Date.now(),
      })
    })

    on('tool_result', (data: unknown) => {
      if (!mounted) return
      if (!isToolResultData(data)) return
      const toolResult = data
      const resultCallIdx = toolResult.call_idx
      const resultRetryAttempt = toolResult.retry_attempt
      const resultRetrySuffix = resultRetryAttempt ? `-r${resultRetryAttempt}` : ''
      const toolMsgId = toolResult.plan_step_id
        ? `tool-${toolResult.plan_step_id}-${toolResult.step}${resultCallIdx !== undefined ? `-${resultCallIdx}` : ''}${resultRetrySuffix}`
        : `tool-${toolResult.step}${resultCallIdx !== undefined ? `-${resultCallIdx}` : ''}${resultRetrySuffix}`
      // Try to update the existing tool_call message
      const msgs = useChatStore.getState().messages[sessionId] || []
      const exists = msgs.some(m => m.id === toolMsgId)
      if (exists) {
        useChatStore.getState().updateMessage(sessionId, toolMsgId, {
          metadata: {
            step: toolResult.step,
            completed: true,
            result: toolResult.result ?? toolResult.result_preview,
            result_preview: toolResult.result_preview,
            result_len: toolResult.result_len,
            plan_step_id: toolResult.plan_step_id,
            call_idx: toolResult.call_idx,
            retry_attempt: toolResult.retry_attempt,
          },
        })
      } else {
        // tool_call message hasn't arrived yet — add as tool_result so groupMessages can buffer it
        useChatStore.getState().addMessage(sessionId, {
          id: `tool-result-${toolMsgId}`,
          sessionId,
          type: 'tool_result',
          content: '',
          metadata: {
            step: toolResult.step,
            completed: true,
            result: toolResult.result ?? toolResult.result_preview,
            result_preview: toolResult.result_preview,
            result_len: toolResult.result_len,
            plan_step_id: toolResult.plan_step_id,
            call_idx: toolResult.call_idx,
            retry_attempt: toolResult.retry_attempt,
          },
          timestamp: Date.now(),
        })
      }
    })

    on('tool_confirm', (data: unknown) => {
      if (!mounted) return
      if (!isToolConfirmData(data)) return
      const toolConfirm = data
      const msgs = useChatStore.getState().messages[sessionId] || []

      // Link to the last tool_call for this tool
      let toolMsgId: string | undefined
      let toolPlanStepId: string | undefined
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]
        if (!m) continue
        if (m.type === 'tool_call' && m.metadata?.tool === toolConfirm.tool) {
          toolMsgId = m.id
          toolPlanStepId = m.metadata?.plan_step_id as string | undefined
          useChatStore.getState().updateMessage(sessionId, m.id, {
            metadata: { ...m.metadata, awaiting_confirmation: true },
          })
          break
        }
      }

      useChatStore.getState().addMessage(sessionId, {
        id: `tool-confirm-${toolConfirm.confirm_id}`,
        sessionId,
        type: 'tool_confirm',
        content: `Confirm: ${toolConfirm.tool}`,
        metadata: {
          confirm_id: toolConfirm.confirm_id,
          tool: toolConfirm.tool,
          args: toolConfirm.args,
          reasoning: toolConfirm.reasoning,
          tool_msg_id: toolMsgId,
          plan_step_id: toolPlanStepId,
        } as Record<string, unknown>,
        timestamp: Date.now(),
      })

      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setActivityStatus('Awaiting confirmation...')
      }
    })

    on('ask_user', (data: unknown) => {
      if (!mounted) return
      if (!isAskUserData(data)) return
      const askData = data
      useChatStore.getState().addMessage(sessionId, {
        id: `ask-user-${askData.request_id}`,
        sessionId,
        type: 'ask_user',
        content: askData.questions.map(q => q.question).join('; '),
        metadata: {
          request_id: askData.request_id,
          questions: askData.questions,
        } as Record<string, unknown>,
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setActivityStatus('Waiting for your answer...')
      }
    })

    on('step_limit', (data: unknown) => {
      if (!mounted) return
      if (!isStepLimitData(data)) return
      const stepLimitData = data
      useChatStore.getState().addMessage(sessionId, {
        id: `step-limit-${stepLimitData.request_id}`,
        sessionId,
        type: 'step_limit',
        content: `Step limit reached: ${stepLimitData.current_step} of ${stepLimitData.max_steps}`,
        metadata: {
          request_id: stepLimitData.request_id,
          current_step: stepLimitData.current_step,
          max_steps: stepLimitData.max_steps,
        } as Record<string, unknown>,
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setActivityStatus('Step limit reached — awaiting decision...')
      }
    })

    on('step_complete', (data: unknown) => {
      if (!mounted) return
      if (!isStepData(data)) return
      if (isActiveSession()) useChatStore.getState().setThinking(false)
      useChatStore.getState().updateMessage(sessionId, `step-${(data as { step_num: number }).step_num}`, {
        type: 'step_done',
      })
    })

    on('reflection', () => {
      if (!mounted) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reflecting on results...')
    })

    on('plan_generated', (data: unknown) => {
      if (!mounted) return
      if (!isPlanData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Executing plan...')
      const plan = data
      // Route to panelStore
      if (plan.steps) {
        panelStore.addPlanGroup(plan.steps, {
          progress: plan.progress,
          completed_count: plan.completed_count,
          total_count: plan.total_count,
        })
      }
      // Also add to chat messages so groupMessages can build stepIndexMap
      useChatStore.getState().addMessage(sessionId, {
        id: `plan-${Date.now()}`,
        sessionId,
        type: 'plan',
        content: '',
        metadata: {
          steps: plan.steps,
          progress: plan.progress,
          current_step_index: plan.current_step_index,
          completed_count: plan.completed_count,
          total_count: plan.total_count,
        },
        timestamp: Date.now(),
      })
    })

    on('plan_step_start', (data: unknown) => {
      if (!mounted) return
      if (!isPlanStepStartData(data)) return
      const stepData = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Executing step ${stepData.step_id}...`)
      // Route to panelStore
      panelStore.updatePlanItemStatus(stepData.step_id, 'running')
      // Add to chat messages for plan step containers
      useChatStore.getState().addMessage(sessionId, {
        id: `plan-step-start-${stepData.step_id}-${Date.now()}`,
        sessionId,
        type: 'plan_step_start',
        content: stepData.description || '',
        metadata: { step_id: stepData.step_id, description: stepData.description },
        timestamp: Date.now(),
      })
    })

    on('plan_step_complete', (data: unknown) => {
      if (!mounted) return
      if (!isPlanStepCompleteData(data)) return
      const stepData = data
      // Route to panelStore
      panelStore.updatePlanItemStatus(
        stepData.step_id,
        stepData.success ? 'completed' : 'failed',
        stepData.duration
      )
      // Add to chat messages for plan step lifecycle
      useChatStore.getState().addMessage(sessionId, {
        id: `plan-step-complete-${stepData.step_id}-${Date.now()}`,
        sessionId,
        type: 'plan_step_complete',
        content: '',
        metadata: {
          step_id: stepData.step_id,
          success: stepData.success,
          duration: stepData.duration,
          ...(stepData.error ? { error: stepData.error } : {}),
        },
        timestamp: Date.now(),
      })
    })

    on('assistant_chunk', (data: unknown) => {
      if (!mounted) return
      if (!isAssistantChunkData(data)) return
      if (!isActiveSession()) return
      useChatStore.getState().setActivityStatus('Generating response...')
      const chunk = data
      if (chunk.accumulated_content !== undefined) {
        // Use backend-accumulated content (direct set, no delta accumulation needed)
        useChatStore.getState().setStreaming(chunk.accumulated_content)
      } else if (chunk.content) {
        // Fallback for older backend: append delta
        useChatStore.getState().appendStreamToken(chunk.content)
      }
    })

    on('assistant_done', () => {
      if (!mounted) return
      const streamingText = useChatStore.getState().streamingText
      if (streamingText) {
        useChatStore.getState().addMessage(sessionId, {
          id: `assistant-${Date.now()}`,
          sessionId,
          type: 'assistant',
          content: streamingText,
          timestamp: Date.now(),
        })
        if (isActiveSession()) useChatStore.getState().setStreaming(null)
      }
    })

    on('error', (data: unknown) => {
      if (!mounted) return
      if (!isErrorData(data)) return
      const error = data
      useChatStore.getState().addMessage(sessionId, {
        id: `error-${Date.now()}`,
        sessionId,
        type: 'error',
        content: error.error || 'An error occurred',
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
    })

    // Task lifecycle events
    on('task_complete', (data: unknown) => {
      if (!mounted) return
      if (!isTaskCompleteData(data)) return
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
      const taskData = data
      // Add completion message
      if (taskData.output) {
        useChatStore.getState().addMessage(sessionId, {
          id: `assistant-done-${Date.now()}`,
          sessionId,
          type: 'assistant',
          content: taskData.output,
          timestamp: Date.now(),
        })
      }
    })

    on('task_cancelled', () => {
      if (!mounted) return
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
      useChatStore.getState().addMessage(sessionId, {
        id: `cancelled-${Date.now()}`,
        sessionId,
        type: 'error',
        content: 'Task was cancelled',
        timestamp: Date.now(),
      })
    })

    on('retry', (data: unknown) => {
      if (!mounted) return
      if (!isRetryData(data)) return
      const retry = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Retrying (attempt ${retry.attempt}/${retry.max_attempts})...`)
      useChatStore.getState().addMessage(sessionId, {
        id: `retry-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Retry attempt ${retry.attempt}/${retry.max_attempts}`,
        metadata: { ...retry },
        timestamp: Date.now(),
      })
      panelStore.updateStats({ attempt: retry.attempt + 1, maxAttempts: retry.max_attempts })
    })

    on('step_retry', (data: unknown) => {
      if (!mounted) return
      if (!isStepRetryData(data)) return
      const stepRetry = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Retrying step ${stepRetry.attempt}/${stepRetry.max_attempts}...`)
      useChatStore.getState().addMessage(sessionId, {
        id: `step-retry-${Date.now()}`,
        sessionId,
        type: 'step_retry',
        content: `Retrying step ${stepRetry.attempt}/${stepRetry.max_attempts}...`,
        metadata: { step_id: stepRetry.step_id, attempt: stepRetry.attempt, max_attempts: stepRetry.max_attempts },
        timestamp: Date.now(),
      })
    })

    on('service', (data: unknown) => {
      if (!mounted) return
      if (!isServiceData(data)) return
      if (!isActiveSession()) return
      const service = data as { content: string; phase?: string }
      if (service.content) {
        useChatStore.getState().setActivityStatus(service.content)
      }
      // Add orchestration phases to chat timeline for visible progress
      if (service.phase === 'orchestration' && service.content) {
        useChatStore.getState().addMessage(sessionId, {
          id: `status-${Date.now()}`,
          sessionId,
          type: 'status',
          content: service.content,
          timestamp: Date.now(),
        })
      }
    })

    on('subagent_launch', (data: unknown) => {
      if (!mounted) return
      if (!isSubAgentLaunchData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Launching sub-agent...')
      const sa = data
      useChatStore.getState().addMessage(sessionId, {
        id: `subagent-${sa.step_id}-launch`,
        sessionId,
        type: 'tool_call',
        content: `SubAgent: ${sa.description}`,
        metadata: { tool: 'subagent', args: sa.description, step: sa.step_id, plan_step_id: sa.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('subagent_complete', (data: unknown) => {
      if (!mounted) return
      if (!isSubAgentCompleteData(data)) return
      const sa = data
      useChatStore.getState().updateMessage(sessionId, `subagent-${sa.step_id}-launch`, {
        metadata: {
          tool: 'subagent',
          completed: true,
          error: sa.success ? undefined : 'SubAgent failed',
          result_preview: sa.success ? `Completed in ${sa.duration}ms` : `Failed after ${sa.duration}ms`,
          result_len: 0,
          plan_step_id: sa.plan_step_id,
        },
      })
    })

    on('context_fill', (data: unknown) => {
      if (!mounted) return
      if (!isContextFillData(data)) return
      if (!isActiveSession()) return
      const fillData = data
      // Store per-step context fill if step_id is present
      if (fillData.plan_step_id) {
        useChatStore.getState().setStepContextFill(fillData.plan_step_id, {
          fillPercent: fillData.fill_percent,
          usedTokens: fillData.used_tokens,
          maxTokens: fillData.max_tokens,
          status: fillData.status,
        })
      }
      // Always update session tokens with model/family
      useChatStore.getState().setSessionTokens(
        fillData.session_input_tokens ?? 0,
        fillData.session_output_tokens ?? 0,
        fillData.model,
        fillData.family
      )
    })

    on('context_compaction', (data: unknown) => {
      if (!mounted) return
      if (!isContextCompactionData(data)) return
      if (!isActiveSession()) return
      useChatStore.getState().addMessage(sessionId, {
        id: `context-compaction-${Date.now()}`,
        sessionId,
        type: 'context_compaction',
        content: `Context compacted from ${Math.round(data.before_percent)}% to ${Math.round(data.after_percent)}%`,
        metadata: { before_percent: data.before_percent, after_percent: data.after_percent, plan_step_id: data.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('session_tokens', (data: unknown) => {
      if (!mounted) return
      if (!isSessionTokensData(data)) return
      if (!isActiveSession()) return
      useChatStore.getState().setSessionTokens(
        data.session_input_tokens,
        data.session_output_tokens,
        data.model,
        data.family
      )
    })

    on('task_failed_resumable', (data: unknown) => {
      if (!mounted) return
      const msg = (data && typeof data === 'object' && 'message' in data && typeof (data as { message: unknown }).message === 'string')
        ? (data as { message: string }).message
        : 'Plan execution failed.'
      useChatStore.getState().addMessage(sessionId, {
        id: `resume-${Date.now()}`,
        sessionId,
        type: 'task_failed_resumable',
        content: msg,
        metadata: { resolved: false },
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        useChatStore.getState().setThinking(false)
        useChatStore.getState().setActivityStatus(null)
      }
    })

    on('task_resumed', () => {
      if (!mounted) return
      useChatStore.getState().resolveResumeMessage(sessionId)
      if (isActiveSession()) {
        useChatStore.getState().setTaskActive(true)
        useChatStore.getState().setActivityStatus('Resuming...')
      }
    })

    on('session_renamed', (data: unknown) => {
      if (!mounted) return
      if (!isSessionRenamedData(data)) return
      const renameData = data
      useSessionStore.getState().updateSession(sessionId, { name: renameData.new_name })
    })

    return () => {
      mounted = false
      unsubs.forEach(fn => fn())
    }
  }, [sessionId, runtime])
}
