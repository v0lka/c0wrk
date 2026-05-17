import type { ComponentType } from 'react'
import type { LucideIcon } from 'lucide-react'
import {
  Terminal, FilePen, FileText, FolderPlus, Trash2,
  Search, FolderOpen, ClipboardCheck, StickyNote,
  Globe, Puzzle, Wrench,
} from 'lucide-react'
import {
  extractBashTitle, extractFileTitle, extractDirTitle,
  extractSearchTitle, extractUrlTitle, extractMemoTitle,
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

const STEP_STATUS_CONFIG: CardConfig = {
  icon: ClipboardCheck, verb: 'Updated',
  extractTitle: () => 'step status',
  Body: MemoBody,
}

const STORE_FACT_CONFIG: CardConfig = {
  icon: StickyNote, verb: 'Stored',
  extractTitle: (args, raw) => extractMemoTitle('store_fact', args, raw),
  Body: MemoBody,
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
  search_facts: SEARCH_CONFIG,
  web_search: SEARCH_CONFIG,
  list_directory: LIST_DIR_CONFIG,
  set_step_status: STEP_STATUS_CONFIG,
  store_fact: STORE_FACT_CONFIG,
  web_fetch: WEB_FETCH_CONFIG,
  read_evidence: SEARCH_CONFIG,
  read_step_output: FILE_READ_CONFIG,
  list_step_outputs: LIST_DIR_CONFIG,
}

export function resolveCardConfig(toolName: string, source?: string): CardConfig {
  if (source && source !== '' && source !== 'core') {
    return { ...MCP_CONFIG, extractTitle: () => toolName }
  }
  return TOOL_CONFIGS[toolName] ?? { ...FALLBACK_CONFIG, extractTitle: () => toolName }
}
