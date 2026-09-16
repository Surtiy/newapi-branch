import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowLeft, BookOpen, Download, FileText, Search } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Markdown } from '@/components/ui/markdown'

import { docsRequest, downloadDoc, renderDoc, type DocsConfig } from './api'
import './api-docs.css'

export function ApiDocs() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['branch-public-docs'], queryFn: () => docsRequest<DocsConfig>('/api/api-docs'), staleTime: 0 })
  const [search, setSearch] = useState('')
  const [activeId, setActiveId] = useState('')
  const page = useRef<HTMLElement>(null)
  const article = useRef<HTMLDivElement>(null)
  const [headings, setHeadings] = useState<Array<{ title: string; id: string; level: string }>>([])
  const baseURL = query.data?.base_url || window.location.origin
  const docs = useMemo(() => (query.data?.docs || []).map(doc => renderDoc(doc, baseURL)), [query.data, baseURL])
  const filtered = docs.filter(doc => [doc.title, doc.description, doc.category, doc.content].join(' ').toLowerCase().includes(search.trim().toLowerCase()))
  const active = filtered.find(doc => doc.id === activeId) || filtered[0]
  const groups = [...new Set(filtered.map(doc => doc.category))]
  useEffect(() => {
    const elements = article.current?.querySelectorAll('h2,h3,h4') || []
    setHeadings(Array.from(elements, (element, index) => {
      const id = `doc-section-${index}`
      return { id, title: element.textContent || '', level: element.tagName.slice(1) }
    }))
  }, [active])

  return <main ref={page} className='api-docs-page'>
    <header className='api-docs-hero'><div className='api-docs-hero-inner'>
      <span className='api-docs-version'>API</span>
      <h1>{t('API Docs', { defaultValue: '接口文档' })}</h1>
      <p>{t('Model requests, asynchronous tasks and results', { defaultValue: '模型调用、异步任务查询和结果获取说明' })}</p>
      <div className='api-docs-hero-actions'>
        <Button className='api-docs-primary-action' render={<Link to='/dashboard' />}><ArrowLeft />{t('Back to Console', { defaultValue: '返回控制台' })}</Button>
        {active && <><Button className='api-docs-secondary-action' onClick={() => downloadDoc(active)}><Download />{t('Download document', { defaultValue: '下载当前文档' })}</Button><CopyButton value={active.content} className='api-docs-secondary-action' size='default'>{t('Copy document', { defaultValue: '复制文档' })}</CopyButton></>}
      </div>
      <div className='api-docs-address'><span>Base URL</span><code>{baseURL}</code><CopyButton value={baseURL} aria-label={t('Copy Base URL')} /></div>
    </div></header>
    <div className='api-docs-layout'>
      <aside className='api-docs-sidebar'>
        <div className='api-docs-sidebar-title'><BookOpen size={17} /><strong>{t('Documentation navigation', { defaultValue: '文档导航' })}</strong></div>
        <div className='api-docs-search'><Search size={15} /><Input aria-label={t('Search documentation', { defaultValue: '搜索接口文档' })} placeholder={t('Search documentation', { defaultValue: '搜索接口文档' })} value={search} onChange={event => setSearch(event.target.value)} /></div>
        <nav className='api-docs-document-nav'>{groups.map(group => <section key={group} className='api-docs-nav-group'>
          <div className='api-docs-nav-category'>{group}</div>
          {filtered.filter(doc => doc.category === group).map(doc => <Button variant='ghost' key={doc.id} aria-current={active?.id === doc.id ? 'page' : undefined} className={active?.id === doc.id ? 'is-active' : ''} onClick={() => { setActiveId(doc.id); page.current?.scrollTo({ top: 0 }) }}><FileText size={14} /><span>{doc.title}</span></Button>)}
        </section>)}</nav>
        {headings.length > 0 && <nav className='api-docs-section-nav'><strong>{t('On this page', { defaultValue: '本页目录' })}</strong>{headings.map((heading, index) => <Button variant='ghost' key={heading.id} className={`level-${heading.level}`} onClick={() => article.current?.querySelectorAll('h2,h3,h4')[index]?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>{heading.title}</Button>)}</nav>}
      </aside>
      <article className='api-docs-content'>
        {query.isPending && <p role='status'>{t('Loading...')}</p>}
        {query.isError && <div role='alert'><p>{t('Failed to load documentation', { defaultValue: '文档加载失败' })}</p><Button onClick={() => query.refetch()}>{t('Retry')}</Button></div>}
        {!query.isPending && !query.isError && !active && <p>{t('No matching documents', { defaultValue: '没有匹配的文档' })}</p>}
        {active && <><header className='api-docs-document-header'><span>{active.category}</span><h2>{active.title}</h2><p>{active.description}</p></header><div ref={article}><Markdown className='api-docs-markdown'>{active.content}</Markdown></div></>}
      </article>
    </div>
  </main>
}
