// RPC method type signatures — grouped by domain

import type {
  ProjectInfo, SessionInfo, ChatMessage, TokenInfo,
  FileEntry, GitStatusEntry, ConfigResponse, SecuritySettingsResponse,
  LLMFullConfigRequest, SearchSettingsRequest,
  MCPServerStatus, MCPServerConfig, ToolInfo,
} from './models'

export interface ProjectAPI {
  createProject(name: string, externalPath?: string): Promise<ProjectInfo>
  deleteProject(id: string): Promise<void>
  renameProject(id: string, name: string): Promise<void>
  listProjects(): Promise<ProjectInfo[]>
  switchProject(id: string): Promise<void>
  pickDirectory(): Promise<string>
}

export interface SessionAPI {
  createSession(): Promise<SessionInfo>
  deleteSession(id: string): Promise<void>
  listSessions(): Promise<SessionInfo[]>
  renameSession(id: string, name: string): Promise<void>
  archiveSession(id: string): Promise<void>
}

export interface ChatAPI {
  sendMessage(sessionId: string, text: string, mode: string, activeSkills?: string[], modelOverride?: string, reasoningOverride?: string): Promise<void>
  cancelTask(sessionId: string): Promise<void>
  cancelUnfinishedTask(sessionId: string): Promise<void>
  getSessionHistory(sessionId: string): Promise<ChatMessage[]>
  getSessionTokens(sessionId: string): Promise<TokenInfo>
  resumeTask(sessionId: string): Promise<void>
}

export interface WorkspaceAPI {
  getSessionWorkspace(sessionId: string): Promise<string>
  listDirectory(path: string, recursive?: boolean): Promise<FileEntry[]>
  getGitStatus(path: string): Promise<Record<string, GitStatusEntry>>
  watchDirectory(path: string): Promise<void>
  unwatchDirectory(path: string): Promise<void>
  readFile(filePath: string): Promise<string>
  getFileDiff(filePath: string): Promise<string>
  getFileIcon(filePath: string): Promise<{ icon: string; icon_color: string }>
}

export interface ConfigAPI {
  getConfig(): Promise<ConfigResponse>
  getSecuritySettings(): Promise<SecuritySettingsResponse>
  updateSecuritySettings(settings: SecuritySettingsResponse): Promise<void>
  updateLLMConfig(req: LLMFullConfigRequest): Promise<void>
  updateSearchSettings(settings: SearchSettingsRequest): Promise<void>
  getLogLevel(): Promise<string>
  setLogLevel(level: string): Promise<void>
}

export interface McpAPI {
  getMCPStatus(): Promise<MCPServerStatus[]>
  getMCPServers(): Promise<Record<string, MCPServerConfig>>
  updateMCPServers(servers: Record<string, MCPServerConfig>): Promise<void>
  getToolList(): Promise<ToolInfo[]>
  listProviderModels(provider: string): Promise<string[]>
}
