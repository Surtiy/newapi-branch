import { api } from '@/lib/api'

export type ApiDoc = { id: string; title: string; description: string; category: string; content: string; filename?: string }
export type DocsConfig = { base_url: string; docs: ApiDoc[] }
export type DocsSettings = { config: DocsConfig; version: string }

export async function docsRequest<T>(path: string, body?: unknown): Promise<T> {
  const response = body === undefined ? await api.get(path) : await api.put(path, body)
  if (!response.data.success) throw new Error(response.data.message || 'Request failed')
  return response.data.data as T
}

export function renderDoc(doc: ApiDoc, baseURL: string): ApiDoc {
  const replace = (value: string) => value.replaceAll('{{BASE_URL}}', baseURL)
  return { ...doc, title: replace(doc.title), description: replace(doc.description), category: replace(doc.category), content: replace(doc.content) }
}

export function normalizeDocsURL(value: string): string {
  const input = value.trim().replace(/\/+$/, '')
  if (!input) return ''
  const url = new URL(input)
  if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password || url.search || url.hash || url.pathname !== '/' || /[\s<>"'`{}\\?#]/.test(input)) throw new Error('Invalid documentation domain')
  return url.origin
}

export function downloadDoc(doc: ApiDoc) {
  const url = URL.createObjectURL(new Blob([doc.content], { type: 'text/markdown;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = (doc.filename || `${doc.id}.md`).replaceAll(/[\\/:*?"<>|]/g, '_')
  anchor.click()
  URL.revokeObjectURL(url)
}
