import type { ComponentType } from 'react'
import type { LucideIcon } from 'lucide-react'
import {
  Terminal, FilePen, FileText, FolderPlus, Trash2,
  Search, FolderOpen, ClipboardCheck, StickyNote,
  Globe, Puzzle, Wrench,
  Brain, Layers, History, ListTree,
  BookOpen,
} from 'lucide-react'
import {
  extractBashTitle, extractFileTitle, extractDirTitle,
  extractSearchTitle, extractUrlTitle, extractMemoTitle,
  extractStepOutputTitle, extractFactsTitle, extractAttachmentTitle,
  extractFileHint, extractBashHint, extractSearchHint, extractMcpHint,
} from './extractors'
import { BashBody } from './bodies/BashBody'
import { FileChangeBody } from './bodies/FileChangeBody'
import { FileReadBody } from './bodies/FileReadBody'
import { SearchBody } from './bodies/SearchBody'
import { ListDirBody } from './bodies/ListDirBody'
import { WebFetchBody } from './bodies/WebFetchBody'
import { McpBody } from './bodies/McpBody'
import { MemoBody } from './bodies/MemoBody'
import { MemoryBody } from './bodies/MemoryBody'
import { GenericBody } from './bodies/GenericBody'

type Args = Record<string, unknown> | undefined

export interface ToolBodyProps {
  parsedArgs?: Record<string, unknown>
  args: string
  result?: string
  resultLen?: number
  status: 'running' | 'success' | 'error' | 'awaiting_confirmation'
}

export interface CardConfig {
  icon: LucideIcon
  verb: string
  extractTitle: (parsedArgs: Args, rawArgs: string) => string
  extractHint?: (parsedArgs: Args, rawArgs: string) => string | undefined
  Body: ComponentType<ToolBodyProps> | null
}

const EXEC_CONFIG: CardConfig = {
  icon: Terminal, verb: 'Executed',
  extractTitle: extractBashTitle, extractHint: extractBashHint,
  Body: BashBody,
}

const FILE_CHANGE_CONFIG: CardConfig = {
  icon: FilePen, verb: 'Changed',
  extractTitle: extractFileTitle, extractHint: extractFileHint,
  Body: FileChangeBody,
}

const FILE_READ_CONFIG: CardConfig = {
  icon: FileText, verb: 'Read',
  extractTitle: extractFileTitle, extractHint: extractFileHint,
  Body: FileReadBody,
}

const DIR_CREATE_CONFIG: CardConfig = {
  icon: FolderPlus, verb: 'Created',
  extractTitle: extractDirTitle, extractHint: extractFileHint,
  Body: null,
}

const DELETE_CONFIG: CardConfig = {
  icon: Trash2, verb: 'Deleted',
  extractTitle: extractDirTitle, extractHint: extractFileHint,
  Body: null,
}

const SEARCH_CONFIG: CardConfig = {
  icon: Search, verb: 'Searched',
  extractTitle: extractSearchTitle, extractHint: extractSearchHint,
  Body: SearchBody,
}

const LIST_DIR_CONFIG: CardConfig = {
  icon: FolderOpen, verb: 'Listed',
  extractTitle: extractDirTitle, extractHint: extractFileHint,
  Body: ListDirBody,
}

const CHECKLIST_CONFIG: CardConfig = {
  icon: ClipboardCheck, verb: 'Updated',
  extractTitle: () => 'checklist',
  Body: MemoBody,
}

const DECLARE_STEP_COMPLETE_CONFIG: CardConfig = {
  icon: ClipboardCheck, verb: 'Marked',
  extractTitle: () => 'step complete',
  Body: MemoBody,
}

const STORE_FACT_CONFIG: CardConfig = {
  icon: StickyNote, verb: 'Stored',
  extractTitle: (args, raw) => extractMemoTitle('store_fact', args, raw),
  Body: MemoryBody,
}

// --- Blackboard / memory operation configs ---
// These read from or query the session blackboard. They get dedicated icons
// and verbs so they never render as misleading "Read: file" file-event cards.

const MEMORY_STEP_CONFIG: CardConfig = {
  icon: Layers, verb: 'Recovered',
  extractTitle: extractStepOutputTitle,
  Body: MemoryBody,
}

const MEMORY_FINAL_CONFIG: CardConfig = {
  icon: History, verb: 'Recovered',
  extractTitle: () => 'previous result',
  Body: MemoryBody,
}

const MEMORY_LIST_CONFIG: CardConfig = {
  icon: ListTree, verb: 'Listed',
  extractTitle: () => 'available steps',
  Body: MemoryBody,
}

const MEMORY_SEARCH_CONFIG: CardConfig = {
  icon: Brain, verb: 'Recalled',
  extractTitle: extractFactsTitle,
  Body: MemoryBody,
}

// read_attachment: compact single-line card (like memory operations).
const READ_ATTACHMENT_CONFIG: CardConfig = {
  icon: BookOpen, verb: 'Read',
  extractTitle: extractAttachmentTitle,
  Body: null,
}

const WEB_FETCH_CONFIG: CardConfig = {
  icon: Globe, verb: 'Fetched',
  extractTitle: extractUrlTitle,
  Body: WebFetchBody,
}

const MCP_CONFIG: CardConfig = {
  icon: Puzzle, verb: 'Called',
  extractTitle: () => '',
  extractHint: extractMcpHint,
  Body: McpBody,
}

const FALLBACK_CONFIG: CardConfig = {
  icon: Wrench, verb: 'Used',
  extractTitle: () => '',
  Body: GenericBody,
}

const TOOL_CONFIGS: Record<string, CardConfig> = {
  bash_exec: EXEC_CONFIG,
  write_file: FILE_CHANGE_CONFIG,
  edit_file: FILE_CHANGE_CONFIG,
  read_file: FILE_READ_CONFIG,
  read_skill_resource: FILE_READ_CONFIG,
  create_directory: DIR_CREATE_CONFIG,
  delete_file: DELETE_CONFIG,
  delete_directory: DELETE_CONFIG,
  glob: SEARCH_CONFIG,
  ripgrep: SEARCH_CONFIG,
  semantic_search: SEARCH_CONFIG,
  web_search: SEARCH_CONFIG,
  list_directory: LIST_DIR_CONFIG,
  update_checklist: CHECKLIST_CONFIG,
  declare_step_complete: DECLARE_STEP_COMPLETE_CONFIG,
  store_fact: STORE_FACT_CONFIG,
  web_fetch: WEB_FETCH_CONFIG,
  // Blackboard / memory operations
  read_step_output: MEMORY_STEP_CONFIG,
  read_final_result: MEMORY_FINAL_CONFIG,
  read_evidence: MEMORY_FINAL_CONFIG,
  list_step_outputs: MEMORY_LIST_CONFIG,
  search_facts: MEMORY_SEARCH_CONFIG,
  read_attachment: READ_ATTACHMENT_CONFIG,
}

const CACHED_SUFFIX = ' (cached)'
const BATCHED_SUFFIX = ' (batched)'

export function resolveCardConfig(toolName: string, source?: string): CardConfig {
  if (source && source !== '' && source !== 'core') {
    return { ...MCP_CONFIG, extractTitle: () => toolName }
  }
  // Handle batched tool results: strip " (batched)" suffix and look up original config.
  if (toolName.endsWith(BATCHED_SUFFIX)) {
    const originalName = toolName.slice(0, -BATCHED_SUFFIX.length)
    return TOOL_CONFIGS[originalName] ?? { ...FALLBACK_CONFIG, extractTitle: () => originalName }
  }
  // Handle cached tool results: strip " (cached)" suffix and look up original config.
  if (toolName.endsWith(CACHED_SUFFIX)) {
    const originalName = toolName.slice(0, -CACHED_SUFFIX.length)
    return TOOL_CONFIGS[originalName] ?? { ...FALLBACK_CONFIG, extractTitle: () => originalName }
  }
  return TOOL_CONFIGS[toolName] ?? { ...FALLBACK_CONFIG, extractTitle: () => toolName }
}
