import { create } from 'zustand'

export type VectorIndexStatus = 'idle' | 'indexing' | 'ready' | 'reindexing'

interface VectorIndexState {
  status: VectorIndexStatus
  progress: number
  filesIndexed: number
  totalFiles: number
  currentFile: string
  branch: string
  updateFromEvent: (data: {
    state: string
    progress: number
    files_indexed: number
    total_files: number
    current_file: string
    branch: string
  }) => void
  reset: () => void
}

function toStatus(state: string): VectorIndexStatus {
  if (state === 'indexing' || state === 'ready' || state === 'reindexing') {
    return state
  }
  return 'idle'
}

export const useVectorIndexStore = create<VectorIndexState>((set) => ({
  status: 'idle',
  progress: 0,
  filesIndexed: 0,
  totalFiles: 0,
  currentFile: '',
  branch: '',
  updateFromEvent: (data) =>
    set({
      status: toStatus(data.state),
      progress: data.progress,
      filesIndexed: data.files_indexed,
      totalFiles: data.total_files,
      currentFile: data.current_file,
      branch: data.branch,
    }),
  reset: () =>
    set({
      status: 'idle',
      progress: 0,
      filesIndexed: 0,
      totalFiles: 0,
      currentFile: '',
      branch: '',
    }),
}))
