/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { tryParseTaskVisualConfig } from '@/features/pricing/lib/task-expr'
import { tryParseVisualConfig } from '@/features/pricing/lib/tier-expr'
import type { BillingUsageSchema } from '@/features/pricing/types'
import {
  ModelPricingEditorPanel,
  type ModelPricingEditorPanelHandle,
  type ModelRatioData,
} from '@/features/system-settings/models/model-pricing-sheet'
import { api } from '@/lib/api'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { pricingRow } from '../pricing'

const clients: QueryClient[] = []

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(
    'model-pricing-preferences',
    JSON.stringify({ state: { currency: 'site' }, version: 0 })
  )
  usePricingPreferencesStore.persist.rehydrate()
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7.3,
    },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [], vendors: [] },
  })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  usePricingPreferencesStore.setState({ currency: 'USD' })
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  localStorage.clear()
})

function renderEditor(
  data: Partial<ModelRatioData> = {},
  usageSchema?: BillingUsageSchema
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], null)
  clients.push(client)
  const ref = createRef<ModelPricingEditorPanelHandle>()
  const dirty = vi.fn()
  const view = (entry: Partial<ModelRatioData>) => (
    <QueryClientProvider client={client}>
      <ModelPricingEditorPanel
        ref={ref}
        editData={{
          name: 'currency-model',
          billingMode: 'per-token',
          ratio: '1',
          completionRatio: '2',
          ...entry,
        }}
        usageSchema={usageSchema}
        isVideoModel={entry.isVideoModel}
        onDirtyChange={dirty}
      />
    </QueryClientProvider>
  )
  const result = render(view(data))
  return {
    ...result,
    ref,
    dirty,
    reload: (entry: Partial<ModelRatioData>) => result.rerender(view(entry)),
  }
}

async function commit(
  ref: React.RefObject<ModelPricingEditorPanelHandle | null>
) {
  let result: ModelRatioData | null = null
  await act(async () => {
    result = (await ref.current?.commitDraft()) ?? null
  })
  return result as ModelRatioData | null
}

it('fills imported scaled prices into the visual editor without changing priority multipliers', async () => {
  const row = pricingRow('gpt-5.5', {
    'billing_setting.billing_mode': 'tiered_expr',
    'billing_setting.billing_expr':
      '((len <= 272000 ? tier("base", p * 5 + c * 30 + cr * 0.5) : tier("long", p * 10 + c * 45 + cr * 1)) * (param("service_tier") == "priority" ? 2.5 : 1)) * 1.2',
  })
  const editor = renderEditor(row)
  expect(await screen.findByText('Visual editor')).toBeVisible()
  const prices = screen.getAllByRole('textbox', { name: 'Input price' })
  expect(prices[0]).toHaveValue('6')
  expect(prices[1]).toHaveValue('12')
  fireEvent.change(prices[0], { target: { value: '7' } })
  const draft = await commit(editor.ref)
  expect(
    tryParseVisualConfig(draft?.billingExpr)?.tiers[0].input_unit_cost
  ).toBe(7)
  expect(draft?.requestRuleExpr).toBe(row.requestRuleExpr)
  expect(tryParseVisualConfig(draft?.billingExpr)?.tiers[0].conditions).toEqual(
    [{ var: 'len', op: '<=', value: 272000 }]
  )
})

it('locks imported flat image price 0.55 to USD despite CNY 7.3 and persisted site currency', async () => {
  const editor = renderEditor({
    name: 'image-model',
    billingMode: 'per-request',
    price: '0.55',
  })
  expect(
    screen.queryByRole('combobox', { name: 'Pricing currency' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'About pricing currency' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(
      'Model prices are fixed in USD. No exchange conversion is applied.'
    )
  ).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  expect(
    within(screen.getByRole('complementary', { name: 'Preview' })).getByText(
      '$0.55'
    )
  ).toBeVisible()
  expect(await commit(editor.ref)).toMatchObject({ price: '0.55' })
})

it('keeps video per-second price in USD through mode switching, model switching and reopening', async () => {
  const saved: Partial<ModelRatioData> = {
    name: 'video-model',
    billingMode: 'per-second',
    price: '0.55',
    isVideoModel: true,
  }
  const editor = renderEditor(saved)
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  expect(
    within(screen.getByRole('complementary', { name: 'Preview' })).getByText(
      '$0.55'
    )
  ).toBeVisible()
  await userEvent.click(screen.getByRole('tab', { name: 'Per-request' }))
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  await userEvent.click(screen.getByRole('tab', { name: 'Per-second' }))
  expect(await commit(editor.ref)).toMatchObject({
    billingMode: 'per-second',
    price: '0.55',
  })
  editor.reload({ name: 'other-image', billingMode: 'per-request', price: '2' })
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue('2')
  editor.reload(saved)
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  editor.unmount()
  await usePricingPreferencesStore.persist.rehydrate()
  const reopened = renderEditor(saved)
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  expect(await commit(reopened.ref)).toMatchObject({ price: '0.55' })
})

it('keeps all language price lanes in USD when site settings change without dirtying the editor', async () => {
  const editor = renderEditor({
    ratio: '0.275',
    completionRatio: '2',
    cacheRatio: '0',
    createCacheRatio: '1',
    imageRatio: '1',
    audioRatio: '2',
    audioCompletionRatio: '2',
  })
  const prices = {
    'Input price': '0.55',
    'Completion price': '1.1',
    'Cache read price': '0',
    'Cache write price': '0.55',
    'Image input price': '0.55',
    'Audio input price': '1.1',
    'Audio output price': '2.2',
  }
  const before = await commit(editor.ref)
  editor.dirty.mockClear()
  act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CUSTOM',
        customCurrencySymbol: '€',
        customCurrencyExchangeRate: 0.5,
      },
    })
  )
  act(() => usePricingPreferencesStore.setState({ currency: 'site' }))
  for (const [name, value] of Object.entries(prices)) {
    expect(screen.getByRole('textbox', { name })).toHaveValue(value)
  }
  expect(await commit(editor.ref)).toEqual(before)
  expect(editor.dirty).not.toHaveBeenCalledWith(true)
  expect(
    within(screen.getByRole('complementary', { name: 'Preview' })).getByText(
      '$2.2'
    )
  ).toBeVisible()
})

it('saves USD token edits while preserving unfinished decimals and disabled lanes', async () => {
  const editor = renderEditor()
  const input = screen.getByRole('textbox', { name: 'Input price' })
  fireEvent.change(input, { target: { value: '3.' } })
  expect(input).toHaveValue('3.')
  fireEvent.change(screen.getByRole('textbox', { name: 'Completion price' }), {
    target: { value: '6' },
  })
  expect(await commit(editor.ref)).toMatchObject({
    ratio: '1.5',
    completionRatio: '2',
    cacheRatio: '',
  })
})

it.each(['0', '0.0000001', '0.55', '14'])(
  'saves fixed USD amount %s without FX or display rounding',
  async (price) => {
    const editor = renderEditor({ billingMode: 'per-request', price: '1' })
    fireEvent.change(screen.getByRole('textbox', { name: 'Fixed price' }), {
      target: { value: price },
    })
    expect(Number((await commit(editor.ref))?.price)).toBe(Number(price))
    expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
      price
    )
  }
)

it.each([0, -1, Infinity, Number.NaN, 1e-308])(
  'ignores invalid or extreme site rate %s for valid USD inputs',
  async (rate) => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: rate,
      },
    })
    const editor = renderEditor({ billingMode: 'per-request', price: '0.55' })
    expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
      '0.55'
    )
    expect(
      screen.queryByText(
        /Current exchange rate:|The site exchange rate is invalid/
      )
    ).not.toBeInTheDocument()
    expect(await commit(editor.ref)).toMatchObject({ price: '0.55' })
  }
)

it('keeps tier coefficients, thresholds, request multipliers and monetary estimates in USD', async () => {
  const editor = renderEditor({
    billingMode: 'tiered_expr',
    billingExpr:
      'len <= 200000 ? tier("short", p * 0.55 + c * 1.1) : tier("long", p * 2 + c * 4)',
    requestRuleExpr: '(header("x-priority") == "high" ? 2 : 1)',
  })
  const before = await commit(editor.ref)
  const inputs = screen.getAllByRole('textbox', { name: 'Input price' })
  expect(inputs[0]).toHaveValue('0.55')
  fireEvent.change(inputs[0], { target: { value: '3' } })
  const saved = await commit(editor.ref)
  expect(tryParseVisualConfig(saved?.billingExpr)?.tiers[0]).toMatchObject({
    input_unit_cost: 3,
    conditions: [{ var: 'len', op: '<=', value: 200000 }],
  })
  expect(saved?.requestRuleExpr).toBe(before?.requestRuleExpr)
  fireEvent.change(screen.getByRole('spinbutton', { name: 'Input tokens' }), {
    target: { value: '1000000' },
  })
  expect(screen.getByText('Estimated cost: $2')).toBeVisible()
})

it('keeps custom raw expressions intact and removes obsolete currency conversion guidance', async () => {
  const expr = 'tier("custom", max(p * 2, 100))'
  const editor = renderEditor({ billingMode: 'tiered_expr', billingExpr: expr })
  expect(await commit(editor.ref)).toMatchObject({ billingExpr: expr })
  expect(
    screen.queryByText(/Currency selection only converts/)
  ).not.toBeInTheDocument()
})

it('saves task base, token, credit and whole-column second prices directly in USD', async () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
    tokens: { type: 'number', unit: 'token' },
    credits: { type: 'number', unit: 'credit' },
    mode: { enum: ['std', 'pro'] },
  }
  const editor = renderEditor(
    {
      billingMode: 'tiered_expr',
      billingExpr: 'tier("base", u("seconds") * 0.55)',
    },
    schema
  )
  expect(
    screen.getByRole('textbox', { name: 'Unit price: seconds: mode: std' })
  ).toHaveValue('0.55')
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Additional charge: mode: std' }),
    { target: { value: '1' } }
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Unit price: tokens: mode: std' }),
    { target: { value: '10' } }
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Unit price: credits: mode: std' }),
    { target: { value: '0.1' } }
  )
  await userEvent.click(
    within(screen.getByRole('columnheader', { name: /seconds/ })).getByRole(
      'button',
      { name: 'Fill entire column' }
    )
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Fill entire column' }),
    { target: { value: '0.55' } }
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Apply to all rows' })
  )
  const saved = await commit(editor.ref)
  const config = tryParseTaskVisualConfig(saved?.billingExpr ?? '', schema)
  expect(config?.tiers[0]).toMatchObject({
    constant: 1,
    unitPrices: { seconds: 0.55, tokens: 10, credits: 0.1 },
  })
  expect(config?.tiers[1].unitPrices.seconds).toBe(0.55)
  expect(saved?.billingExpr).toContain('/ 1000000')
})

it('shows task monetary preview in USD and accepts fractional preview quantities', async () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
  }
  const editor = renderEditor(
    {
      billingMode: 'tiered_expr',
      billingExpr: 'tier("base", u("seconds") * 0.55)',
    },
    schema
  )
  expect(screen.getByRole('textbox', { name: 'seconds' })).toHaveValue('0.55')
  fireEvent.change(screen.getByRole('textbox', { name: 'Additional charge' }), {
    target: { value: '1' },
  })
  fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '0.5' } })
  expect(screen.getByText(/= \$1.275$/)).toBeVisible()
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: 'tier("base", 1 + u("seconds") * 0.55)',
  })
})

it('keeps an empty fixed amount empty through a site config update', async () => {
  const editor = renderEditor({
    billingMode: 'per-request',
    price: '1',
    ratio: '',
    completionRatio: '',
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Fixed price' }), {
    target: { value: '' },
  })
  act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 3,
      },
    })
  )
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue('')
  expect(await commit(editor.ref)).toMatchObject({ price: '' })
})

it('blocks non-finite amount drafts and resets them on reload of the same USD value', async () => {
  const saved: Partial<ModelRatioData> = {
    billingMode: 'per-request',
    price: '0.55',
    ratio: '',
    completionRatio: '',
  }
  const editor = renderEditor(saved)
  fireEvent.change(screen.getByRole('textbox', { name: 'Fixed price' }), {
    target: { value: '9'.repeat(309) },
  })
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveAttribute(
    'aria-invalid',
    'true'
  )
  expect(await commit(editor.ref)).toBeNull()
  editor.reload({ ...saved })
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.55'
  )
  expect(await commit(editor.ref)).toMatchObject({ price: '0.55' })
})
