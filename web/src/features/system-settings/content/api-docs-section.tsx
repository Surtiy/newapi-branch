import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, Save } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Markdown } from '@/components/ui/markdown'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { docsRequest, normalizeDocsURL, renderDoc, type DocsSettings } from '@/features/api-docs/api'

import { SettingsSection } from '../components/settings-section'

function DocsEditor(props: { settings: DocsSettings }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [config, setConfig] = useState(props.settings.config)
  const [version, setVersion] = useState(props.settings.version)
  const [selected, setSelected] = useState(config.docs[0]?.id || '')
  const active = config.docs.find(doc => doc.id === selected)
  const [error, setError] = useState('')
  let previewURL = window.location.origin
  try { previewURL = normalizeDocsURL(config.base_url) || previewURL } catch { /* Keep the last valid preview origin. */ }
  const rendered = active ? renderDoc(active, previewURL) : undefined
  const save = useMutation({
    mutationFn: async () => {
      let baseURL: string
      try { baseURL = normalizeDocsURL(config.base_url) } catch { throw new Error(t('Enter an HTTP or HTTPS domain without paths, keys or query parameters', { defaultValue: '请输入 http/https 域名，不要带 /v1、密钥或查询参数' })) }
      return docsRequest<DocsSettings>('/api/option/branch_docs', { version, config: { ...config, base_url: baseURL } })
    },
    onSuccess: result => {
      setVersion(result.version); setConfig(result.config); setError('')
      client.setQueryData(['branch-docs-settings'], result)
      void client.invalidateQueries({ queryKey: ['branch-public-docs'] })
      toast.success(t('Setting updated successfully'))
    },
    onError: cause => setError(cause.message),
  })
  const update = (field: 'title' | 'description' | 'category' | 'content', value: string) => setConfig(previous => ({ ...previous, docs: previous.docs.map(doc => doc.id === selected ? { ...doc, [field]: value } : doc) }))
  return <div className='space-y-5'>
    <div className='flex flex-wrap items-end gap-3'>
      <div className='min-w-0 flex-1 space-y-2'><Label htmlFor='docs-domain'>{t('Documentation API domain', { defaultValue: '文档接口域名' })}</Label><Input id='docs-domain' value={config.base_url} onChange={event => setConfig(previous => ({ ...previous, base_url: event.target.value }))} placeholder='https://api.example.com' disabled={save.isPending} aria-describedby='docs-domain-hint' /></div>
      <Button disabled={save.isPending} onClick={() => save.mutate()}><Save />{t('Save')}</Button>
      <Button variant='outline' render={<a href='/api-docs' target='_blank' rel='noreferrer' />}><ExternalLink />{t('View documentation', { defaultValue: '查看文档' })}</Button>
    </div>
    <p id='docs-domain-hint' className='text-sm text-muted-foreground'>{t('Leave blank to use the current domain. All examples and downloads use this address.', { defaultValue: '留空使用当前访问域名。文档示例和下载内容统一使用此地址。' })}</p>
    <div className='flex min-w-0 items-center gap-2 text-sm'><span>Base URL:</span><code className='break-all'>{previewURL}</code><CopyButton value={previewURL} /></div>
    {error && <p role='alert' className='text-destructive'>{error}</p>}
    <div className='space-y-2'><Label htmlFor='docs-selected'>{t('Document', { defaultValue: '文档' })}</Label><NativeSelect id='docs-selected' value={selected} onChange={event => setSelected(event.target.value)}>{config.docs.map(doc => <NativeSelectOption key={doc.id} value={doc.id}>{doc.title}</NativeSelectOption>)}</NativeSelect></div>
    {active && <>
      <div className='grid gap-3 sm:grid-cols-2'><div className='space-y-2'><Label htmlFor='doc-title'>{t('Title')}</Label><Input id='doc-title' value={active.title} disabled={save.isPending} onChange={event => update('title', event.target.value)} /></div><div className='space-y-2'><Label htmlFor='doc-category'>{t('Category')}</Label><Input id='doc-category' value={active.category} disabled={save.isPending} onChange={event => update('category', event.target.value)} /></div></div>
      <div className='space-y-2'><Label htmlFor='doc-description'>{t('Description')}</Label><Input id='doc-description' value={active.description} disabled={save.isPending} onChange={event => update('description', event.target.value)} /></div>
      <div className='grid min-w-0 gap-5 xl:grid-cols-2'><div className='min-w-0 space-y-2'><Label htmlFor='doc-content'>{t('Document content', { defaultValue: '文档内容' })}</Label><Textarea id='doc-content' value={active.content} disabled={save.isPending} onChange={event => update('content', event.target.value)} className='h-[600px] resize-y font-mono text-sm' /><p className='text-sm text-muted-foreground'>{t('Use {{BASE_URL}} for the documentation domain', { defaultValue: '接口域名使用占位符 {{BASE_URL}}', interpolation: { skipOnVariables: true } })}</p></div><div className='min-w-0 space-y-2'><Label>{t('Preview')}</Label><div className='max-h-[600px] overflow-auto border p-4'><Markdown>{rendered?.content || ''}</Markdown></div></div></div>
    </>}
  </div>
}

export function ApiDocsSection() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['branch-docs-settings'], queryFn: () => docsRequest<DocsSettings>('/api/option/branch_docs') })
  return <SettingsSection title={t('API Docs', { defaultValue: '接口文档管理' })}>
    {query.isPending && <p>{t('Loading...')}</p>}
    {query.isError && <div role='alert'><p>{query.error.message}</p><Button onClick={() => query.refetch()}>{t('Retry')}</Button></div>}
    {query.data && <DocsEditor settings={query.data} />}
  </SettingsSection>
}
