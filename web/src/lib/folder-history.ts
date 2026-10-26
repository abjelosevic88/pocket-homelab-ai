import { api, qs, V1 } from './api'

export interface FolderHistory {
  folders: { path: string }[]
  selected_path: string | null
}

export const folderHistory = (host = 0) => api.get<FolderHistory>(`${V1}/remembered-folders${qs({ host })}`)
export const rememberFolder = (path: string, host = 0) => api.post<FolderHistory>(`${V1}/remembered-folders`, { host_id: host, path })
export const forgetFolder = (path: string, host = 0) => api.del<FolderHistory>(`${V1}/remembered-folders${qs({ host, path })}`)
export const restoredFolder = (history: FolderHistory, fallback: string) => history.selected_path ?? history.folders[0]?.path ?? fallback
