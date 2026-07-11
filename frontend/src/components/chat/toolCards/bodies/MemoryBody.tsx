import React from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { TruncatedContent } from '../shared/TruncatedContent'

/**
 * MemoryBody renders the content of blackboard / memory operations:
 * read_step_output, list_step_outputs, read_final_result, search_facts,
 * and store_fact. For read/search operations the meaningful payload lives in
 * `result`; for store_fact (a write) it lives in the `content` argument.
 *
 * Uses TruncatedContent so large recovered outputs (e.g. delegation step
 * dumps) collapse instead of flooding the chat.
 */
export const MemoryBody = React.memo(function MemoryBody({ result, status, parsedArgs, args }: ToolBodyProps) {
  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-accent/30 bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Accessing memory...</span>
      </div>
    )
  }

  // store_fact exposes the stored payload via args.content; read/search ops via result.
  const parsed = parsedArgs ?? tryParse(args)
  const storedContent = parsed?.content as string | undefined
  const display = result || storedContent

  if (!display) return null

  return (
    <div className="mt-2 border-l-2 border-accent/30 bg-muted/30 rounded p-3 min-w-0">
      <TruncatedContent content={display} maxLines={30} />
    </div>
  )
})

function tryParse(s: string): Record<string, unknown> | undefined {
  try { return JSON.parse(s) } catch { return undefined }
}
