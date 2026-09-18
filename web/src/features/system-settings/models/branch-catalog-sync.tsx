import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { Download, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { ModelPricingEntry } from '@/features/model-pricing/api'
import type { PricingValues } from '@/features/model-pricing/pricing'
import { api } from '@/lib/api'

import { SettingsSection } from '../components/settings-section'
import type { PricingSyncValues } from '../types'
import { SyncPriceCell } from './upstream-price-cells'

type CatalogRow = {
  model_name: string
  description?: string
  model_type: string
  video_billing_unit?: string
  local: ModelPricingEntry
  incoming: PricingValues
  imported: boolean
  changed: boolean
  blocked?: string
  groups: CatalogGroup[]
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
}
type BranchConfig = {
  main_url: string
  can_configure: boolean
  channels: Array<{ id: number; name: string; configured: boolean }>
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
  values: PricingValues
  video: boolean
  ratio?: number
}) {
  const { t } = useTranslation()
  const ratio = props.ratio ?? 1
  const expression = props.values['billing_setting.billing_expr']
  if (
    props.video &&
    props.values['billing_setting.billing_mode'] === 'tiered_expr' &&
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
  const values = { ...props.values }
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
  const [channelId, setChannelId] = useState(0)
  const [apiKey, setApiKey] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [updatePrices, setUpdatePrices] = useState<Record<string, boolean>>({})
  const [selectedGroups, setSelectedGroups] = useState<
    Record<string, string[]>
  >({})
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
  const activeChannel = channelId || config.data?.channels[0]?.id || 0
  const configured =
    config.data?.channels.find((channel) => channel.id === activeChannel)
      ?.configured ?? false
  const saveKey = useMutation({
    mutationFn: () =>
      request<{ channel_id: number }>('/api/option/branch_catalog/config', {
        channel_id: activeChannel,
        api_key: apiKey.trim(),
      }),
    onSuccess: async (data) => {
      setApiKey('')
      setChannelId(data.channel_id)
      setPreview(null)
      setSelected({})
      await queryClient.invalidateQueries({
        queryKey: ['branch-catalog-channels'],
      })
      toast.success(t('Main site API key saved'))
    },
    onError: showError,
  })
  const fetchCatalog = useMutation({
    mutationFn: () =>
      request<Preview>('/api/option/branch_catalog/preview', {
        channel_id: activeChannel,
      }),
    onSuccess: (data) => {
      setPreview(data)
      setSelected({})
      setUpdatePrices(
        Object.fromEntries(
          data.models.map((row) => [
            row.model_name,
            Object.keys(row.local.configured).length === 0,
          ])
        )
      )
      setSelectedGroups(
        Object.fromEntries(
          data.models.map((row) => [
            row.model_name,
            row.groups.map((group) => group.name),
          ])
        )
      )
      setUpdateRatios(
        Object.fromEntries(
          data.models.flatMap((row) =>
            row.groups.map((group) => [group.name, group.local.ratio === null])
          )
        )
      )
      setPage(0)
    },
    onError: showError,
  })
  const apply = useMutation({
    mutationFn: () =>
      request<{ count: number }>('/api/option/branch_catalog/apply', {
        channel_id: activeChannel,
        catalog_version: preview?.catalog_version,
        channel_version: preview?.channel_version,
        price_multiplier: Number(priceMultiplier),
        models: preview?.models
          .filter((row) => selected[row.model_name])
          .map((row) => ({
            model_name: row.model_name,
            expected_version: row.local.version,
            update_price:
              Number(priceMultiplier) !== 1 || !!updatePrices[row.model_name],
            groups: selectedGroups[row.model_name] || [],
          })),
        groups: activeGroups.map((group) => ({
          name: group.name,
          expected_version: group.local.version,
          update_ratio: !!updateRatios[group.name],
        })),
      }),
    onSuccess: (data) => {
      toast.success(t('Synced {{count}} models', { count: data.count }))
      setConfirm(false)
      void queryClient.invalidateQueries({ queryKey: ['model-pricing'] })
      fetchCatalog.mutate()
    },
    onError: (error) => {
      setConfirm(false)
      showError(error)
    },
  })
  const busy = fetchCatalog.isPending || apply.isPending || saveKey.isPending
  const parsedPriceMultiplier = Number(priceMultiplier)
  const validPriceMultiplier =
    priceMultiplier.trim() !== '' &&
    Number.isFinite(parsedPriceMultiplier) &&
    parsedPriceMultiplier > 0 &&
    parsedPriceMultiplier <= 1000
  const appliesUniformPrice =
    validPriceMultiplier && parsedPriceMultiplier !== 1
  const filtered = useMemo(
    () =>
      (preview?.models || []).filter(
        (row) =>
          (kind === 'all' || row.model_type === kind) &&
          row.model_name.toLowerCase().includes(search.toLowerCase().trim())
      ),
    [preview, kind, search]
  )
  const count = Object.values(selected).filter(Boolean).length
  const updateCount = Object.keys(selected).filter(
    (name) => selected[name] && (appliesUniformPrice || updatePrices[name])
  ).length
  const visible = filtered.slice(page * 20, (page + 1) * 20)
  const eligible = filtered.filter((row) => !row.blocked)
  const allSelected =
    eligible.length > 0 && eligible.every((row) => selected[row.model_name])
  const activeGroups = [
    ...new Map(
      (preview?.models || [])
        .filter((row) => selected[row.model_name])
        .flatMap((row) =>
          row.groups.filter((group) =>
            selectedGroups[row.model_name]?.includes(group.name)
          )
        )
        .map((group) => [group.name, group])
    ).values(),
  ]
  const missingGroups = (preview?.models || []).some(
    (row) => selected[row.model_name] && !selectedGroups[row.model_name]?.length
  )
  const ratioCount = activeGroups.filter(
    (group) => updateRatios[group.name]
  ).length

  return (
    <SettingsSection title={t('Main Site Model Sync')}>
      <FieldGroup className='gap-3'>
        <Field orientation='horizontal' className='flex-wrap'>
          <FieldLabel>{t('Main site')}</FieldLabel>
          <span>https://aicost.me</span>
        </Field>
        {(config.data?.channels.length ?? 0) > 1 && (
          <Field orientation='horizontal' className='flex-wrap'>
            <FieldLabel htmlFor='branch-main-channel'>
              {t('Main site channel')}
            </FieldLabel>
            <NativeSelect
              id='branch-main-channel'
              value={activeChannel}
              disabled={busy}
              onChange={(event) => {
                setChannelId(Number(event.target.value))
                setApiKey('')
                setPreview(null)
                setSelected({})
              }}
            >
              {config.data?.channels.map((channel) => (
                <NativeSelectOption key={channel.id} value={channel.id}>
                  {channel.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </Field>
        )}
        <Field orientation='horizontal' className='flex-wrap'>
          <FieldLabel htmlFor='branch-main-api-key'>
            {t('Main site API key')}
          </FieldLabel>
          <PasswordInput
            id='branch-main-api-key'
            className='w-full sm:max-w-md'
            value={apiKey}
            autoComplete='new-password'
            spellCheck={false}
            maxLength={4096}
            disabled={busy || !config.data?.can_configure}
            placeholder={t(
              configured
                ? 'Configured; enter a new key to replace'
                : 'Enter your main site API key'
            )}
            onChange={(event) => {
              setApiKey(event.target.value)
              setPreview(null)
              setSelected({})
            }}
          />
          {config.data?.can_configure && (
            <Button
              disabled={busy || !apiKey.trim()}
              onClick={() => saveKey.mutate()}
            >
              {t(saveKey.isPending ? 'Saving...' : 'Save API key')}
            </Button>
          )}
          <Button
            disabled={!activeChannel || !configured || busy || !!apiKey.trim()}
            onClick={() => fetchCatalog.mutate()}
          >
            <RefreshCw
              className={
                fetchCatalog.isPending ? 'size-4 animate-spin' : 'size-4'
              }
            />
            {t('Fetch main site models')}
          </Button>
        </Field>
      </FieldGroup>
      <p className='text-muted-foreground text-sm'>
        {t(
          configured
            ? 'Main site API key is configured. Stored keys are never displayed.'
            : 'Save a main site API key to start syncing models.'
        )}
      </p>
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
      <p className='text-muted-foreground text-sm'>
        {t(
          'Manual sync only. Fetch to preview; select models and confirm before applying. Existing prices are kept unless selected for update.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Group multipliers are shared by models in the same group. Existing multipliers are preserved unless selected for update.'
        )}
      </p>
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
                '1 keeps the main site price. Other values update every selected model price.'
              )}
            </span>
            {!validPriceMultiplier && (
              <span role='alert' className='text-destructive text-sm'>
                {t('Enter a multiplier greater than 0 and no more than 1000.')}
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
                count: preview.models.length,
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
            getRowKey={(row) => row.model_name}
            emptyContent={t('No matching models')}
            columns={[
              {
                id: 'select',
                header: (
                  <Checkbox
                    aria-label={t('Select filtered models')}
                    disabled={busy || !eligible.length}
                    checked={allSelected}
                    onCheckedChange={(checked) =>
                      setSelected((previous) => ({
                        ...previous,
                        ...Object.fromEntries(
                          eligible.map((row) => [row.model_name, !!checked])
                        ),
                      }))
                    }
                  />
                ),
                cell: (row) => (
                  <Checkbox
                    aria-label={t('Select {{model}}', {
                      model: row.model_name,
                    })}
                    disabled={busy || !!row.blocked}
                    checked={!!selected[row.model_name]}
                    onCheckedChange={(checked) =>
                      setSelected((previous) => ({
                        ...previous,
                        [row.model_name]: !!checked,
                      }))
                    }
                  />
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
                    {row.description && (
                      <p className='text-muted-foreground text-xs break-words'>
                        {row.description}
                      </p>
                    )}
                    <span className='text-muted-foreground text-xs'>
                      {row.imported ? t('Already imported') : t('New model')}
                    </span>
                    {row.blocked && (
                      <p className='text-destructive text-xs'>{row.blocked}</p>
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
                id: 'groups',
                header: t('Groups and multipliers'),
                cellClassName: 'min-w-72 max-w-96 whitespace-normal',
                cell: (row) => (
                  <details>
                    <summary className='cursor-pointer'>
                      {t('Selected groups')}:{' '}
                      {selectedGroups[row.model_name]?.length || 0} /{' '}
                      {row.groups.length}
                    </summary>
                    <div className='mt-3 space-y-3'>
                      {row.groups.map((group) => (
                        <div
                          key={group.name}
                          className='border-b pb-3 last:border-b-0'
                        >
                          <label className='flex items-start gap-2'>
                            <Checkbox
                              disabled={busy}
                              aria-label={`${row.model_name} / ${group.name}`}
                              checked={
                                selectedGroups[row.model_name]?.includes(
                                  group.name
                                ) || false
                              }
                              onCheckedChange={(checked) =>
                                setSelectedGroups((previous) => ({
                                  ...previous,
                                  [row.model_name]: checked
                                    ? [
                                        ...new Set([
                                          ...(previous[row.model_name] || []),
                                          group.name,
                                        ]),
                                      ]
                                    : (previous[row.model_name] || []).filter(
                                        (name) => name !== group.name
                                      ),
                                }))
                              }
                            />
                            <span className='break-all'>{group.name}</span>
                          </label>
                          <div className='text-muted-foreground mt-1 text-xs'>
                            {t('Main site multiplier')}: {group.ratio} ·{' '}
                            {t('Branch multiplier')}:{' '}
                            {group.local.ratio ?? t('New group')}
                          </div>
                          <div className='mt-2'>
                            <CatalogPrice
                              values={row.incoming}
                              video={row.model_type === 'video'}
                              ratio={group.ratio}
                            />
                          </div>
                          <label className='mt-2 flex items-center gap-2 text-xs'>
                            <Checkbox
                              aria-label={`${t('Update group multiplier')} ${group.name} / ${row.model_name}`}
                              disabled={busy || group.local.ratio === null}
                              checked={!!updateRatios[group.name]}
                              onCheckedChange={(checked) =>
                                setUpdateRatios((previous) => ({
                                  ...previous,
                                  [group.name]: !!checked,
                                }))
                              }
                            />
                            {t('Update group multiplier')}
                            {group.imported && (
                              <span className='text-muted-foreground'>
                                {' '}
                                · {t('Already imported')}
                              </span>
                            )}
                          </label>
                        </div>
                      ))}
                    </div>
                  </details>
                ),
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
                        appliesUniformPrice || !!updatePrices[row.model_name]
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
                          [row.model_name]: !!checked,
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
              {page + 1} / {Math.max(1, Math.ceil(filtered.length / 20))}
            </span>
            <Button
              variant='outline'
              disabled={(page + 1) * 20 >= filtered.length}
              onClick={() => setPage((value) => value + 1)}
            >
              {t('Next')}
            </Button>
          </div>
        </>
      )}
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
