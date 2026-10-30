// Files on this server or one of your machines: a file opens in a tab of its
// own ("file:<host>:<path>"), next to conversations and terminals.
import { V1 } from './api'

export interface FileEntry { name: string; dir: boolean; link?: boolean; size: number; mtime: number }
export interface FileListing { host_id: number; host: string; path: string; home: string; parent?: string; entries: FileEntry[]; more?: boolean }

export const FILE_TAB = 'file:'
export const isFileTab = (id: string) => id.startsWith(FILE_TAB)
export const fileTab = (host: number, path: string) => `${FILE_TAB}${host}:${path}`
export const parseFileTab = (id: string) => {
  const rest = id.slice(FILE_TAB.length)
  const i = rest.indexOf(':')
  return { host: Number(rest.slice(0, i)) || 0, path: rest.slice(i + 1) }
}
export const filePath = (host: number, path: string) => `/f/${host}?path=${encodeURIComponent(path)}`
export const filesPath = (host: number, path = '') => `/files?host=${host}${path ? `&path=${encodeURIComponent(path)}` : ''}`
export const rawURL = (host: number, path: string, download = false) =>
  `${V1}/files/raw?host=${host}&path=${encodeURIComponent(path)}${download ? '&download=1' : ''}`

export const baseOf = (p: string) => p.replace(/\/+$/, '').split('/').pop() || p
export const dirOf = (p: string) => { const d = p.replace(/\/+$/, '').split('/').slice(0, -1).join('/'); return d || '/' }
export const joinPath = (dir: string, name: string) => (dir.endsWith('/') ? dir : dir + '/') + name

export type FileKind = 'image' | 'pdf' | 'video' | 'audio' | 'markdown' | 'text'
export function fileKind(name: string): FileKind {
  const ext = name.toLowerCase().split('.').pop() || ''
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'avif', 'bmp', 'ico'].includes(ext)) return 'image'
  if (ext === 'pdf') return 'pdf'
  if (['mp4', 'webm', 'mov', 'm4v', 'mkv', 'ogv'].includes(ext)) return 'video'
  if (['mp3', 'm4a', 'wav', 'ogg', 'oga', 'flac', 'aac', 'opus'].includes(ext)) return 'audio'
  if (['md', 'markdown', 'mdx'].includes(ext)) return 'markdown'
  return 'text'
}
export const fileIcon = (name: string, dir = false) => dir ? 'folder' : ({ image: 'image', pdf: 'file', video: 'video', audio: 'play', markdown: 'file-edit', text: 'file' } as const)[fileKind(name)]

/** Shell-quotes a path for typing it into a terminal. */
export const shq = (s: string) => /^[A-Za-z0-9_./=:,+@%-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`

/** Uploads files into a folder; onProgress gets 0..1. Resolves with where they went. */
export function uploadFiles(host: number, dir: string, files: File[], onProgress?: (p: number) => void): Promise<string[]> {
  return new Promise((resolve, reject) => {
    const fd = new FormData()
    for (const f of files) fd.append('file', f, f.name)
    const x = new XMLHttpRequest()
    x.open('POST', `${V1}/files/upload?host=${host}&path=${encodeURIComponent(dir)}`)
    x.setRequestHeader('X-Pocket-Source', 'web')
    x.upload.onprogress = e => { if (e.lengthComputable) onProgress?.(e.loaded / e.total) }
    x.onload = () => {
      let d: { paths?: string[]; error?: string } = {}
      try { d = JSON.parse(x.responseText) } catch { /* not JSON */ }
      if (x.status >= 200 && x.status < 300) resolve(d.paths || [])
      else reject(new Error(d.error || (x.status === 413 ? 'Too big to upload' : `Upload failed (${x.status})`)))
    }
    x.onerror = () => reject(new Error('Upload failed: the connection broke'))
    x.send(fd)
  })
}
