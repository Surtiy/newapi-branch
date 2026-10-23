import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import {
  Download,
  Folder,
  KeyRound,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import type { ModelPricingEntry } from '@/features/model-pricing/api'
import type { PricingValues } from '@/features/model-pricing/pricing'
import { api } from '@/lib/api'

import { SettingsSection } from '../components/settings-section'
import type { PricingSyncValues } from '../types'
import { BranchCatalogKeyDialog } from './branch-catalog-key-dialog'
import { SyncPriceCell } from './upstream-price-cells'

type CatalogRow = {
  source_id: number
  key_preview: string
  model_name: string
  description?: string
  model_type: string
  video_billing_unit?: string
  local: ModelPricingEntry
  incoming: PricingValues | null
  imported: boolean
  changed: boolean
  blocked?: string
  groups: CatalogGroup[]
  conflicting_groups?: string[]
}
type CatalogGroup = {
  name: string
  ratio: number
  local: { ratio: number | null; version: string }
  imported: boolean
}
type Preview = {
  catalog_version: string
  channel_version: string
  models: CatalogRow[]
  group_ratio: Record<string, number>
  bound_groups?: string[]
}
type CatalogSnapshot = {
  sources: Array<{ channel_id: number; key_preview: string; preview: Preview }>
  models: CatalogRow[]
}
function rowKey(row: CatalogRow): string {
  return `${row.source_id}:${row.model_name}`
}

function rowsOverlap(left: CatalogRow, right: CatalogRow): boolean {
  return (
    left.model_name === right.model_name &&
    left.groups.some((group) =>
      right.groups.some((other) => other.name === group.name)
    )
  )
}
type BranchConfig = {
  main_url: string
  can_configure: boolean
  channels: Array<{
    id: number
    name: string
    configured: boolean
    key_preview?: string
    bound_group?: string
    bound_groups?: string[]
    channel_version: string
    is_source?: boolean
  }>
}

type Envelope<T> = { success: boolean; message?: string; data: T }

async function request<T>(path: string, body?: unknown): Promise<T> {
  const response =
    body === undefined
      ? await api.get<Envelope<T>>(path, {
          skipErrorHandler: true,
          skipBusinessError: true,
        })
      : await api.post<Envelope<T>>(path, body, {
          skipErrorHandler: true,
          skipBusinessError: true,
        })
  if (!response.data.success) {
    throw new Error(response.data.message || 'Sync failed')
  }
  return response.data.data
}

function displayPrice(values: PricingValues): PricingSyncValues {
  const keys: Record<string, string> = {
    ModelPrice: 'model_price',
    ModelRatio: 'model_ratio',
    CompletionRatio: 'completion_ratio',
    CacheRatio: 'cache_ratio',
    CreateCacheRatio: 'create_cache_ratio',
    ImageRatio: 'image_ratio',
    AudioRatio: 'audio_ratio',
    AudioCompletionRatio: 'audio_completion_ratio',
    'billing_setting.billing_mode': 'billing_mode',
    'billing_setting.billing_expr': 'billing_expr',
  }
  return Object.fromEntries(
    Object.entries(values).map(([key, value]) => [keys[key], value])
  ) as PricingSyncValues
}

function CatalogPrice(props: {
  values: PricingValues | null
  video: boolean
  ratio?: number
}) {
  const { t } = useTranslation()
  const ratio = props.ratio ?? 1
  const values = { ...props.values }
  const expression = values['billing_setting.billing_expr']
  if (
    props.video &&
    values['billing_setting.billing_mode'] === 'tiered_expr' &&
    typeof expression === 'string'
  ) {
    const match =
      /^tier\("(request|second)",\s*(u\("seconds"\)\s*\*\s*)?([\d.eE+-]+)\)$/.exec(
        expression.trim()
      )
    if (
      match &&
      (match[1] === 'second') === !!match[2] &&
      Number.isFinite(Number(match[3]))
    ) {
      return (
        <span className='font-medium whitespace-nowrap tabular-nums'>
          ${Number((Number(match[3]) * ratio).toPrecision(12))} /{' '}
          {t(match[1] === 'second' ? 'second' : 'request')}
        </span>
      )
    }
  }
  for (const key of ['ModelPrice', 'ModelRatio'] as const) {
    if (typeof values[key] === 'number') values[key] *= ratio
  }
  return (
    <div>
      <SyncPriceCell values={displayPrice(values)} />
      {ratio !== 1 &&
        values['billing_setting.billing_mode'] === 'tiered_expr' && (
          <span className='text-muted-foreground text-xs'> × {ratio}</span>
        )}
    </div>
  )
}

export function BranchCatalogSync() {
  const { t } = useTranslation()
  const showError = (error: unknown) => {
    let message = ''
    if (isAxiosError(error)) {
      message = error.response?.data?.message || error.message
    } else if (error instanceof Error) message = error.message
    toast.error(message || t('Request failed'))
  }
  const queryClient = useQueryClient()
  const [keyEditor, setKeyEditor] = useState<number | null>(null)
  const [deleteSource, setDeleteSource] = useState<
    BranchConfig['channels'][number] | null
  >(null)
  const [deleteError, setDeleteError] = useState('')
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [updatePrices, setUpdatePrices] = useState<Record<string, boolean>>({})
  const [updateRatios, setUpdateRatios] = useState<Record<string, boolean>>({})
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState('all')
  const [priceMultiplier, setPriceMultiplier] = useState('1')
  const [confirm, setConfirm] = useState(false)
  const [page, setPage] = useState(0)
  const config = useQuery({
    queryKey: ['branch-catalog-channels'],
    queryFn: () => request<BranchConfig>('/api/option/branch_catalog'),
  })
  const boundSources =
    config.data?.channels.filter(
      (channel) =>
        channel.is_source || channel.bound_groups?.length || channel.bound_group
    ) || []
  const configured = boundSources.some((source) => source.configured)
  const catalog = useQuery({
    queryKey: [
      'branch-catalog-preview',
      boundSources.map((source) => source.id).join(','),
    ],
    queryFn: async () => {
      const snapshot: CatalogSnapshot = { sources: [], models: [] }
      for (const source of boundSources) {
        const data = await request<Preview>(
          '/api/option/branch_catalog/preview',
          { channel_id: source.id }
        )
        if (!data || !Array.isArray(data.models)) {
          throw new Error(
            `${source.key_preview || source.name}: ${t('Invalid main site model catalog')}`
          )
        }
        const keyPreview = source.key_preview || source.name
        snapshot.sources.push({
          channel_id: source.id,
          key_preview: keyPreview,
          preview: data,
        })
        snapshot.models.push(
          ...data.models.map((row) => ({
            ...row,
            source_id: source.id,
            key_preview: keyPreview,
          }))
        )
      }
      return snapshot
    },
    enabled: configured,
    retry: false,
    refetchOnWindowFocus: false,
  })
  const preview = configured && !catalog.isError ? catalog.data || null : null
  const directoryCandidates = (preview?.models || []).filter(
    (row) => row.groups.length > 1
  )
  const conflicts = useMemo(() => {
    const names = new Map<
      string,
      { model_name: string; group_name: string; owners: CatalogRow[] }
    >()
    for (const row of preview?.models || []) {
      for (const group of row.groups) {
        const key = JSON.stringify([row.model_name, group.name])
        const entry = names.get(key) || {
          model_name: row.model_name,
          group_name: group.name,
          owners: [],
        }
        if (!entry.owners.some((owner) => owner.source_id === row.source_id)) {
          entry.owners.push(row)
        }
        names.set(key, entry)
      }
    }
    return [...names.values()].filter((entry) => entry.owners.length > 1)
  }, [preview])
  useEffect(() => {
    if (preview) {
      const data = preview
      setSelected({})
      setUpdatePrices(
        Object.fromEntries(
          data.models.map((row) => [
            rowKey(row),
            Object.keys(row.local.configured).length === 0,
          ])
        )
      )
      setUpdateRatios(
        Object.fromEntries(
          data.models.flatMap((row) =>
            row.groups.map((group) => [
              `${row.source_id}:${group.name}`,
              group.local.ratio === null,
            ])
          )
        )
      )
      setPage(0)
    }
  }, [preview])
  const apply = useMutation({
    mutationFn: async () => {
      let count = 0
      const writtenGroups = new Set<string>()
      // Base prices are shared across groups; only accept versions this batch wrote.
      const writtenModels = new Map<string, string>()
      const incomingPrices = new Map<string, string>()
      for (const source of preview?.sources || []) {
        const rows =
          preview?.models.filter(
            (row) =>
              row.source_id === source.channel_id && selected[rowKey(row)]
          ) || []
        if (!rows.length) continue
        try {
          const fresh = await request<Preview>(
            '/api/option/branch_catalog/preview',
            { channel_id: source.channel_id }
          )
          if (
            fresh.catalog_version !== source.preview.catalog_version ||
            fresh.channel_version !== source.preview.channel_version
          ) {
            throw new Error(
              t('Main site catalog changed. Fetch it again before syncing.')
            )
          }
          const modelVersions = new Map<string, string>()
          for (const row of rows) {
            const freshRow = fresh.models.find(
              (candidate) => candidate.model_name === row.model_name
            )
            const writtenVersion = writtenModels.get(row.model_name)
            const incoming = JSON.stringify(
              row.incoming,
              Object.keys(row.incoming || {}).sort()
            )
            if (
              !freshRow ||
              (writtenVersion &&
                (freshRow.local.version !== writtenVersion ||
                  incomingPrices.get(row.model_name) !== incoming))
            ) {
              throw new Error(
                t('Model pricing changed. Fetch it again before syncing.')
              )
            }
            modelVersions.set(
              row.model_name,
              writtenVersion || row.local.version
            )
          }
          const groups = [
            ...new Map(
              rows
                .flatMap((row) => row.groups)
                .map((group) => [group.name, group])
            ).values(),
          ]
          const result = await request<{
            count: number
            pricing_versions?: Record<string, string>
          }>('/api/option/branch_catalog/apply', {
            channel_id: source.channel_id,
            catalog_version: fresh.catalog_version,
            channel_version: fresh.channel_version,
            price_multiplier: Number(priceMultiplier),
            models: rows.map((row) => ({
              model_name: row.model_name,
              expected_version: modelVersions.get(row.model_name),
              update_price: true,
              groups: row.groups.map((group) => group.name),
            })),
            groups: groups.map((group) => ({
              name: group.name,
              expected_version: writtenGroups.has(group.name)
                ? fresh.models
                    .flatMap((row) => row.groups)
                    .find((candidate) => candidate.name === group.name)?.local
                    .version
                : group.local.version,
              update_ratio:
                !!updateRatios[`${source.channel_id}:${group.name}`],
            })),
          })
          count += result.count
          for (const group of groups) writtenGroups.add(group.name)
          for (const row of rows) {
            const version = result.pricing_versions?.[row.model_name]
            if (version) {
              writtenModels.set(row.model_name, version)
              incomingPrices.set(
                row.model_name,
                JSON.stringify(
                  row.incoming,
                  Object.keys(row.incoming || {}).sort()
                )
              )
            }
          }
        } catch (error) {
          let message = t('Request failed')
          if (isAxiosError(error)) {
            message = error.response?.data?.message || error.message
          } else if (error instanceof Error) {
            message = error.message
          }
          throw new Error(
            `${t('Synced {{count}} models', { count })}. ${source.key_preview}: ${message}`
          )
        }
      }
      return { count }
    },
    onSuccess: (data) => {
      toast.success(t('Synced {{count}} models', { count: data.count }))
      setConfirm(false)
      void queryClient.invalidateQueries({ queryKey: ['model-pricing'] })
      void catalog.refetch()
      void queryClient.invalidateQueries({
        queryKey: ['branch-catalog-channels'],
      })
    },
    onError: (error) => {
      setConfirm(false)
      showError(error)
      void catalog.refetch()
    },
  })
  const removeSource = useMutation({
    mutationFn: (channel: BranchConfig['channels'][number]) =>
      request<{ disabled_channels: number }>(
        '/api/option/branch_catalog/delete',
        {
          channel_id: channel.id,
          channel_version: channel.channel_version,
        }
      ),
    onSuccess: (_data, channel) => {
      queryClient.setQueryData<BranchConfig>(
        ['branch-catalog-channels'],
        (previous) =>
          previous
            ? {
                ...previous,
                channels: previous.channels.filter(
                  (row) => row.id !== channel.id
                ),
              }
            : previous
      )
      queryClient.removeQueries({ queryKey: ['branch-catalog-preview'] })
      setSelected({})
      setSearch('')
      setKind('all')
      setPage(0)
      setDeleteSource(null)
      setDeleteError('')
      toast.success(t('API key binding deleted'))
      void queryClient.invalidateQueries({
        queryKey: ['branch-catalog-channels'],
      })
      void queryClient.invalidateQueries({ queryKey: ['model-pricing'] })
      void queryClient.invalidateQueries({ queryKey: ['channels'] })
    },
    onError: (error) => {
      let message = t('Request failed')
      if (isAxiosError(error)) {
        message = error.response?.data?.message || error.message
      } else if (error instanceof Error) {
        message = error.message
      }
      setDeleteError(message)
    },
  })
  const busy = catalog.isFetching || apply.isPending || removeSource.isPending
  const parsedPriceMultiplier = Number(priceMultiplier)
  const validPriceMultiplier =
    priceMultiplier.trim() !== '' &&
    Number.isFinite(parsedPriceMultiplier) &&
    parsedPriceMultiplier > 0 &&
    parsedPriceMultiplier <= 1000
  const appliesUniformPrice = validPriceMultiplier
  const filtered = useMemo(
    () =>
      (preview?.models || [])
        .filter(
          (row) =>
            (kind === 'all' || row.model_type === kind) &&
            row.model_name.toLowerCase().includes(search.toLowerCase().trim())
        )
        .sort(
          (left, right) =>
            left.model_name.localeCompare(right.model_name, 'en', {
              numeric: true,
            }) || left.source_id - right.source_id
        ),
    [preview, kind, search]
  )
  const count = Object.values(selected).filter(Boolean).length
  const updateCount = Object.keys(selected).filter(
    (name) => selected[name] && (appliesUniformPrice || updatePrices[name])
  ).length
  // Keep every source of the same model on one page for group comparison.
  const modelNames = [...new Set(filtered.map((row) => row.model_name))]
  const pageNames = new Set(modelNames.slice(page * 20, (page + 1) * 20))
  const visible = filtered.filter((row) => pageNames.has(row.model_name))
  const pageCount = Math.max(1, Math.ceil(modelNames.length / 20))
  const eligible = filtered.filter((row) => !row.blocked)
  const allSelected =
    eligible.length > 0 &&
    eligible.every((row) =>
      eligible.some(
        (candidate) =>
          rowsOverlap(candidate, row) && selected[rowKey(candidate)]
      )
    )
  const activeGroups = [
    ...new Map(
      (preview?.models || [])
        .filter((row) => selected[rowKey(row)])
        .flatMap((row) =>
          row.groups.map((group) => ({ ...group, source_id: row.source_id }))
        )
        .map((group) => [`${group.source_id}:${group.name}`, group])
    ).values(),
  ]
  const missingGroups = (preview?.models || []).some(
    (row) => selected[rowKey(row)] && !row.groups.length
  )
  const ratioCount = activeGroups.filter(
    (group) => updateRatios[`${group.source_id}:${group.name}`]
  ).length

  return (
    <SettingsSection title={t('Main Site Model Sync')}>
      <div className='flex flex-wrap items-center justify-between gap-3 border-b pb-4'>
        <div className='flex items-center gap-3'>
          <h3 className='text-base font-semibold'>AICost</h3>
          <span className='text-muted-foreground text-sm'>aicost.me</span>
        </div>
        {config.data?.can_configure && (
          <Button
            variant='outline'
            disabled={busy}
            onClick={() => setKeyEditor(0)}
          >
            <Plus className='size-4' />
            {t('Add API key')}
          </Button>
        )}
      </div>
      <div className='grid min-w-0 gap-4 md:grid-cols-[240px_minmax(0,1fr)] md:gap-6'>
        <aside
          aria-label={t('Main site groups')}
          className='min-w-0 border-b pb-4 md:border-r md:border-b-0 md:pr-4'
        >
          <h3 className='mb-3 flex items-center gap-2 text-sm font-semibold'>
            <Folder className='size-4' />
            {t('Main site groups')}
            <span className='text-muted-foreground ml-auto tabular-nums'>
              {boundSources.reduce(
                (total, channel) =>
                  total +
                  (preview?.sources.find(
                    (source) => source.channel_id === channel.id
                  )?.preview.bound_groups?.length ??
                    channel.bound_groups?.length ??
                    channel.bound_group?.split(',').length ??
                    0),
                0
              )}
            </span>
          </h3>
          {config.isPending && <LoadingState size='sm' className='min-h-24' />}
          <div className='max-h-64 space-y-4 overflow-y-auto md:max-h-[calc(100vh-260px)]'>
            {boundSources.map((channel, index) => {
              const current = preview?.sources.find(
                (source) => source.channel_id === channel.id
              )?.preview
              const groups =
                current?.bound_groups ??
                (current?.group_ratio
                  ? Object.keys(current.group_ratio).sort()
                  : channel.bound_groups ||
                    channel.bound_group?.split(',') ||
                    [])
              return (
                <div key={channel.id} className='min-w-0'>
                  <div className='mb-1 flex items-center justify-between gap-2'>
                    <span className='text-muted-foreground flex min-w-0 flex-1 items-center gap-2 text-xs'>
                      <KeyRound className='size-3.5 shrink-0' />
                      <span
                        className={
                          channel.key_preview
                            ? 'font-mono break-all'
                            : undefined
                        }
                      >
                        {channel.key_preview ||
                          t('API key {{index}}', { index: index + 1 })}
                      </span>
                    </span>
                    {config.data?.can_configure && (
                      <div className='flex shrink-0 items-center'>
                        <Tooltip>
                          <TooltipTrigger
                            render={
                              <Button
                                variant='ghost'
                                size='icon'
                                className='size-7'
                                aria-label={t('Replace API key {{index}}', {
                                  index: index + 1,
                                })}
                                disabled={busy}
                                onClick={() => setKeyEditor(channel.id)}
                              >
                                <Pencil className='size-3.5' />
                              </Button>
                            }
                          />
                          <TooltipContent>
                            {t('Replace API key')}
                          </TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger
                            render={
                              <Button
                                variant='ghost'
                                size='icon'
                                className='text-destructive size-7'
                                aria-label={t('Delete API key {{index}}', {
                                  index: index + 1,
                                })}
                                disabled={busy}
                                onClick={() => {
                                  setDeleteError('')
                                  setDeleteSource(channel)
                                }}
                              >
                                <Trash2
                                  className='size-3.5'
                                  aria-hidden='true'
                                />
                              </Button>
                            }
                          />
                          <TooltipContent>{t('Delete API key')}</TooltipContent>
                        </Tooltip>
                      </div>
                    )}
                  </div>
                  <ul className='space-y-1'>
                    {groups.map((name) => {
                      const group = current?.models
                        .flatMap((row) => row.groups)
                        .find((group) => group.name === name)
                      return (
                        <li key={name} className='min-h-8 px-2 py-1.5 text-sm'>
                          <div className='min-w-0 break-words'>{name}</div>
                          {group && (
                            <div className='text-muted-foreground mt-1 flex items-center gap-2 text-xs'>
                              <span className='min-w-0 flex-1 break-words'>
                                {t('Main site multiplier')}: {group.ratio} /{' '}
                                {t('Branch multiplier')}:{' '}
                                {group.local.ratio ?? t('New group')}
                              </span>
                              <Checkbox
                                aria-label={t(
                                  'Update multiplier for {{group}}',
                                  { group: name }
                                )}
                                disabled={busy || group.local.ratio === null}
                                checked={
                                  !!updateRatios[`${channel.id}:${name}`]
                                }
                                onCheckedChange={(checked) =>
                                  setUpdateRatios((previous) => ({
                                    ...previous,
                                    [`${channel.id}:${name}`]: !!checked,
                                  }))
                                }
                              />
                            </div>
                          )}
                        </li>
                      )
                    })}
                  </ul>
                </div>
              )
            })}
          </div>
          {!config.isPending && !boundSources.length && (
            <p className='text-muted-foreground py-4 text-sm'>
              {t('No groups')}
            </p>
          )}
        </aside>
        <div
          className='min-w-0 space-y-4'
          aria-label={t('Main site models')}
          role='region'
        >
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div className='min-w-0'>
              <h3 className='text-base font-semibold'>
                {t('Main site models')}
              </h3>
            </div>
            <Button
              variant='outline'
              disabled={!configured || busy}
              onClick={() => {
                setSelected({})
                void catalog.refetch()
                void queryClient.invalidateQueries({
                  queryKey: ['branch-catalog-channels'],
                })
              }}
            >
              <RefreshCw
                className={
                  catalog.isFetching ? 'size-4 animate-spin' : 'size-4'
                }
              />
              {t('Fetch main site models')}
            </Button>
          </div>
          {catalog.isPending && configured && <LoadingState />}
          {catalog.isError && (
            <div role='alert' className='text-destructive text-sm break-words'>
              {catalog.error instanceof Error
                ? catalog.error.message
                : t('Request failed')}
            </div>
          )}
          {!configured && !config.isPending && (
            <EmptyState icon={KeyRound} title={t('No main site models')} />
          )}
          {directoryCandidates.length > 0 && (
            <Alert
              role='note'
              aria-label={t('Review candidate directory groups')}
            >
              <AlertTitle>{t('Review candidate directory groups')}</AlertTitle>
              <AlertDescription>
                <p>
                  {t(
                    'The main site catalog does not identify actual token bindings. Check these candidate groups against your token settings before syncing prices.'
                  )}
                </p>
                <ul className='mt-2 space-y-1'>
                  {directoryCandidates.map((row) => (
                    <li key={rowKey(row)} className='break-words'>
                      {t('{{model}} · {{key}}: {{groups}}', {
                        model: row.model_name,
                        key: row.key_preview,
                        groups: row.groups
                          .map((group) => group.name)
                          .join(' / '),
                        interpolation: { escapeValue: false },
                      })}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}
          {conflicts.length > 0 && (
            <div
              role='alert'
              className='border-destructive/40 border-l-2 pl-3 text-sm'
            >
              <p className='text-destructive font-medium'>
                {t('Duplicate model names in the same group')}
              </p>
              <ul className='mt-2 space-y-1'>
                {conflicts.map((conflict) => (
                  <li
                    key={JSON.stringify([
                      conflict.model_name,
                      conflict.group_name,
                    ])}
                    className='break-words'
                  >
                    <span className='font-medium'>{conflict.model_name}</span>:{' '}
                    {conflict.owners.map((owner) => (
                      <div key={owner.source_id} className='mt-1 text-xs'>
                        <span className='font-mono'>{owner.key_preview}</span> ·{' '}
                        {t('Main site groups')}: {conflict.group_name}
                      </div>
                    ))}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {config.data && !config.data.can_configure && (
            <p className='text-muted-foreground text-sm'>
              {t(
                'Ask an administrator with sensitive channel permissions to configure the API key.'
              )}
            </p>
          )}
          {config.isError && (
            <p role='alert' className='text-destructive'>
              {t('Failed to load main site channels')}
            </p>
          )}
          {preview && (
            <>
              <Field orientation='horizontal' className='flex-wrap'>
                <FieldLabel htmlFor='branch-price-multiplier'>
                  {t('Unified price multiplier')}
                </FieldLabel>
                <Input
                  id='branch-price-multiplier'
                  className='w-32'
                  type='number'
                  min='0.01'
                  max='1000'
                  step='0.01'
                  inputMode='decimal'
                  value={priceMultiplier}
                  disabled={busy}
                  aria-invalid={!validPriceMultiplier}
                  onChange={(event) => setPriceMultiplier(event.target.value)}
                />
                <span className='text-muted-foreground text-sm'>
                  {t(
                    'Every selected model price is updated using this multiplier, including 1.'
                  )}
                </span>
                {!validPriceMultiplier && (
                  <span role='alert' className='text-destructive text-sm'>
                    {t(
                      'Enter a multiplier greater than 0 and no more than 1000.'
                    )}
                  </span>
                )}
              </Field>
              <div className='flex flex-wrap items-center gap-3'>
                <Input
                  className='max-w-xs'
                  aria-label={t('Search models')}
                  placeholder={t('Search models')}
                  value={search}
                  onChange={(event) => {
                    setSearch(event.target.value)
                    setPage(0)
                  }}
                />
                <NativeSelect
                  aria-label={t('Model type')}
                  value={kind}
                  onChange={(event) => {
                    setKind(event.target.value)
                    setPage(0)
                  }}
                >
                  <NativeSelectOption value='all'>
                    {t('All models')}
                  </NativeSelectOption>
                  <NativeSelectOption value='video'>
                    {t('Video')}
                  </NativeSelectOption>
                  <NativeSelectOption value='image'>
                    {t('Image')}
                  </NativeSelectOption>
                  <NativeSelectOption value='language'>
                    {t('Language')}
                  </NativeSelectOption>
                  <NativeSelectOption value='other'>
                    {t('Other')}
                  </NativeSelectOption>
                </NativeSelect>
                <span className='text-muted-foreground text-sm'>
                  {t('{{count}} models available', {
                    count: filtered.length,
                  })}
                </span>
                <Button
                  className='sm:ml-auto'
                  disabled={
                    !count || busy || missingGroups || !validPriceMultiplier
                  }
                  onClick={() => setConfirm(true)}
                >
                  <Download className='size-4' />
                  {t('Confirm selected models')} ({count})
                </Button>
              </div>
              <StaticDataTable<CatalogRow>
                data={visible}
                getRowKey={rowKey}
                emptyContent={t('No matching models')}
                columns={[
                  {
                    id: 'select',
                    header: (
                      <div className='flex items-center gap-2'>
                        <Checkbox
                          aria-label={t('Select filtered models')}
                          disabled={busy || !eligible.length}
                          checked={allSelected}
                          onCheckedChange={(checked) =>
                            setSelected((previous) => {
                              const next = { ...previous }
                              const chosen: CatalogRow[] = []
                              for (const row of eligible) {
                                next[rowKey(row)] =
                                  !!checked &&
                                  !chosen.some((other) =>
                                    rowsOverlap(row, other)
                                  )
                                if (next[rowKey(row)]) chosen.push(row)
                              }
                              return next
                            })
                          }
                        />
                        <span>{t('Sync')}</span>
                      </div>
                    ),
                    cell: (row) => (
                      <div className='flex items-center gap-2'>
                        <Checkbox
                          aria-label={t('Select {{model}}', {
                            model: row.model_name,
                          })}
                          disabled={busy || !!row.blocked}
                          checked={!!selected[rowKey(row)]}
                          onCheckedChange={(checked) =>
                            setSelected((previous) => {
                              const next = { ...previous }
                              for (const other of preview.models) {
                                if (checked && rowsOverlap(other, row)) {
                                  next[rowKey(other)] = false
                                }
                              }
                              next[rowKey(row)] = !!checked
                              return next
                            })
                          }
                        />
                        <span className='text-muted-foreground text-xs'>
                          {t('Sync')}
                        </span>
                      </div>
                    ),
                  },
                  {
                    id: 'model',
                    header: t('Model'),
                    cellClassName: 'min-w-48 max-w-72 whitespace-normal',
                    cell: (row) => (
                      <div className='space-y-1'>
                        <div className='font-medium break-all'>
                          {row.model_name}
                        </div>
                        <div className='text-muted-foreground font-mono text-xs break-all'>
                          {row.key_preview}
                        </div>
                        <div className='text-muted-foreground text-xs break-words'>
                          {row.groups.map((group) => group.name).join(' / ')}
                        </div>
                        {row.description && (
                          <p className='text-muted-foreground text-xs break-words'>
                            {row.description}
                          </p>
                        )}
                        <span className='text-muted-foreground text-xs'>
                          {row.imported
                            ? t('Already imported')
                            : t('New model')}
                        </span>
                        {row.blocked && (
                          <p className='text-destructive text-xs'>
                            {row.blocked}
                          </p>
                        )}
                      </div>
                    ),
                  },
                  {
                    id: 'billing',
                    header: t('Billing unit'),
                    cell: (row) => {
                      if (row.model_type === 'video') {
                        return row.video_billing_unit === 'second'
                          ? t('Per second')
                          : t('Per request')
                      }
                      if (
                        row.incoming?.['billing_setting.billing_mode'] ===
                        'tiered_expr'
                      ) {
                        return t('Tiered pricing')
                      }
                      return row.incoming?.ModelPrice !== undefined
                        ? t('Per request')
                        : t('Tokens')
                    },
                  },
                  {
                    id: 'main',
                    header: t('Main site price'),
                    cellClassName: 'min-w-40 max-w-96 whitespace-normal',
                    cell: (row) =>
                      row.incoming ? (
                        <CatalogPrice
                          values={row.incoming}
                          video={row.model_type === 'video'}
                        />
                      ) : (
                        '—'
                      ),
                  },
                  {
                    id: 'local',
                    header: t('Branch price'),
                    cellClassName: 'min-w-40 max-w-96 whitespace-normal',
                    cell: (row) => (
                      <CatalogPrice
                        values={row.local.effective}
                        video={row.model_type === 'video'}
                      />
                    ),
                  },
                  {
                    id: 'multiplied',
                    header: t('Price after multiplier'),
                    cellClassName: 'min-w-40 max-w-96 whitespace-normal',
                    cell: (row) =>
                      validPriceMultiplier ? (
                        <CatalogPrice
                          values={row.incoming}
                          video={row.model_type === 'video'}
                          ratio={parsedPriceMultiplier}
                        />
                      ) : (
                        '—'
                      ),
                  },
                  {
                    id: 'update',
                    header: t('Update price'),
                    cell: (row) => (
                      <div className='flex items-center gap-2'>
                        <Checkbox
                          aria-label={t('Update price for {{model}}', {
                            model: row.model_name,
                          })}
                          checked={
                            appliesUniformPrice || !!updatePrices[rowKey(row)]
                          }
                          disabled={
                            busy ||
                            appliesUniformPrice ||
                            !!row.blocked ||
                            Object.keys(row.local.configured).length === 0
                          }
                          onCheckedChange={(checked) =>
                            setUpdatePrices((previous) => ({
                              ...previous,
                              [rowKey(row)]: !!checked,
                            }))
                          }
                        />
                        <span className='text-muted-foreground text-xs'>
                          {row.changed ? t('Different') : t('Unchanged')}
                        </span>
                      </div>
                    ),
                  },
                ]}
              />
              <div className='flex items-center justify-end gap-3 text-sm'>
                <Button
                  variant='outline'
                  disabled={page === 0}
                  onClick={() => setPage((value) => value - 1)}
                >
                  {t('Previous')}
                </Button>
                <span>
                  {page + 1} / {pageCount}
                </span>
                <Button
                  variant='outline'
                  disabled={page + 1 >= pageCount}
                  onClick={() => setPage((value) => value + 1)}
                >
                  {t('Next')}
                </Button>
              </div>
            </>
          )}
        </div>
      </div>
      {config.data?.can_configure && (
        <BranchCatalogKeyDialog
          channelId={keyEditor}
          groups={
            boundSources.find((channel) => channel.id === keyEditor)
              ?.bound_groups || []
          }
          onClose={() => setKeyEditor(null)}
          onSaved={() => {
            setSelected({})
            setPage(0)
            void queryClient.invalidateQueries({
              queryKey: ['branch-catalog-preview'],
            })
          }}
        />
      )}
      <ConfirmDialog
        open={deleteSource !== null}
        onOpenChange={(open) => {
          if (!open && !removeSource.isPending) {
            setDeleteSource(null)
            setDeleteError('')
          }
        }}
        title={t('Delete API key')}
        desc={
          <div className='space-y-2'>
            <p className='font-mono break-all'>
              {deleteSource?.key_preview || deleteSource?.name}
            </p>
            <p className='break-words'>
              {t('Groups')}:{' '}
              {deleteSource?.bound_groups?.join(', ') ||
                deleteSource?.bound_group}
            </p>
            <p>
              {t(
                'Remove this branch binding and disable its synced channels. Existing tasks, history and prices are retained. The main site API key is not revoked.'
              )}
            </p>
          </div>
        }
        confirmText={t('Delete')}
        destructive
        disabled={!config.data?.can_configure}
        isLoading={removeSource.isPending}
        handleConfirm={() => {
          if (deleteSource && !removeSource.isPending) {
            setDeleteError('')
            removeSource.mutate(deleteSource)
          }
        }}
      >
        {deleteError && (
          <p role='alert' className='text-destructive text-sm break-words'>
            {deleteError}
          </p>
        )}
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t('Confirm model sync')}
        desc={t(
          'Sync {{count}} models, update {{prices}} prices with a {{multiplier}} multiplier, and update {{ratios}} group multipliers. Group multiplier changes affect all models in those groups.',
          {
            count,
            prices: updateCount,
            multiplier: parsedPriceMultiplier,
            ratios: ratioCount,
          }
        )}
        confirmText={t('Apply sync')}
        isLoading={apply.isPending}
        handleConfirm={() => apply.mutate()}
      />
    </SettingsSection>
  )
}
