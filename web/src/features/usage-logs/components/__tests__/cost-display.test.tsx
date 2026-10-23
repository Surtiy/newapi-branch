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
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import type React from 'react'
import { I18nextProvider } from 'react-i18next'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
} from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { formatLogQuota } from '@/lib/format'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { LogCostDisplay } from '../log-cost-display'

function renderCost(
  props: React.ComponentProps<typeof LogCostDisplay>
): ReturnType<typeof render> {
  return render(<LogCostDisplay {...props} />)
}

function normalizedText(value: string | null): string {
  return (value ?? '').replaceAll(/\s/g, '')
}

describe('log cost display', () => {
  const previousConfig = useSystemConfigStore.getState().config

  beforeEach(() => {
    useSystemConfigStore
      .getState()
      .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  })

  afterEach(() => {
    useSystemConfigStore.getState().setConfig(previousConfig)
  })

  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      Subscription: 'Subscription',
      'Deducted by subscription': 'Deducted by subscription',
      'Includes tool-call surcharge': 'Includes tool-call surcharge',
    })
  })

  test('keeps the regular cost visible and adds an accessible surcharge marker', () => {
    const rendered = renderCost({
      logType: 2,
      quota: 12500,
      other: {
        tool_surcharges: [{ name: 'lookup_customer', count: 1, price: 5 }],
      },
    })

    expect(
      normalizedText(rendered.container.textContent).includes(
        normalizedText(formatLogQuota(12500))
      )
    ).toBe(true)
    const marker = screen.getByRole('img', {
      name: 'Includes tool-call surcharge',
    })
    expect(marker).toHaveAttribute('data-tool-surcharge-indicator', 'true')
    expect(marker).toHaveAttribute('tabindex', '0')
  })

  test('preserves the subscription badge and adds the same legacy surcharge marker', () => {
    renderCost({
      logType: 2,
      quota: 5000,
      other: {
        billing_source: 'subscription',
        web_search: true,
        web_search_call_count: 1,
        web_search_price: 10,
      },
    })

    expect(screen.getByText('Subscription')).toBeInTheDocument()
    expect(
      screen.getByRole('img', { name: 'Includes tool-call surcharge' })
    ).toHaveAttribute('data-tool-surcharge-indicator', 'true')
  })

  test.each([
    { logType: 2, label: 'Consumed quota', opposite: 'Refunded quota' },
    { logType: 6, label: 'Refunded quota', opposite: 'Consumed quota' },
  ])(
    'labels type=$logType by ledger direction without inferring another amount',
    (scenario) => {
      renderCost({ logType: scenario.logType, quota: 12500, other: null })

      const amount = screen.getByRole('group', { name: scenario.label })
      expect(normalizedText(amount.textContent)).toContain('$0.025')
      expect(screen.queryByText(scenario.opposite)).not.toBeInTheDocument()
    }
  )

  test.each([0, 1, -12500])(
    'preserves the recorded quota=%s, including zero, tiny and signed values',
    (quota) => {
      renderCost({ logType: 6, quota, other: null })
      expect(
        normalizedText(
          screen.getByRole('group', { name: 'Refunded quota' }).textContent
        )
      ).toContain(normalizedText(formatLogQuota(quota)))
    }
  )

  test('shows subscription refund quota without claiming a deduction or tool surcharge', () => {
    renderCost({
      logType: 6,
      quota: 5000,
      other: {
        billing_source: 'subscription',
        tool_surcharges: [{ name: 'lookup_customer', count: 1, price: 5 }],
      },
    })

    const amount = screen.getByRole('group', { name: 'Refunded quota' })
    expect(normalizedText(amount.textContent)).toContain('$0.01')
    expect(screen.getByText('Subscription')).toBeVisible()
    expect(
      screen.queryByRole('img', { name: 'Includes tool-call surcharge' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Consumed quota')).not.toBeInTheDocument()
  })

  test('shows the consumed subscription quota directly instead of only on hover', () => {
    renderCost({
      logType: 2,
      quota: 5000,
      other: { billing_source: 'subscription' },
    })
    expect(
      normalizedText(
        screen.getByRole('group', { name: 'Consumed quota' }).textContent
      )
    ).toContain('$0.01')
    expect(screen.getByText('Subscription')).toBeVisible()
  })

  test('describes a subscription refund as returned quota when focused with the keyboard', async () => {
    const user = userEvent.setup()
    renderCost({
      logType: 6,
      quota: 5000,
      other: { billing_source: 'subscription' },
    })
    await user.tab()
    expect(screen.getByText('Subscription').closest('[tabindex]')).toHaveFocus()
    expect(await screen.findByText(/Refunded to subscription/)).toBeVisible()
    expect(
      screen.queryByText(/Deducted by subscription/)
    ).not.toBeInTheDocument()
  })

  test.each([0, 5])(
    'does not label type=%s as consumption or refund',
    (logType) => {
      renderCost({ logType, quota: 0, other: null })
      expect(screen.getByRole('group', { name: 'Cost' })).toBeVisible()
      expect(screen.queryByText('Consumed quota')).not.toBeInTheDocument()
      expect(screen.queryByText('Refunded quota')).not.toBeInTheDocument()
    }
  )

  test('uses the configured currency conversion for a refund without changing its ledger amount', () => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
      },
    })
    renderCost({ logType: 6, quota: 500000, other: null })
    expect(
      normalizedText(
        screen.getByRole('group', { name: 'Refunded quota' }).textContent
      )
    ).toContain('¥7')
  })

  test('switches the quota direction labels between English and Chinese', async () => {
    const i18n = i18next.createInstance()
    await i18n.init({ lng: 'en', fallbackLng: 'en', resources: { en, zh } })
    render(
      <I18nextProvider i18n={i18n}>
        <LogCostDisplay logType={2} quota={12500} other={null} />
        <LogCostDisplay logType={6} quota={5000} other={null} />
      </I18nextProvider>
    )

    expect(screen.getByRole('group', { name: 'Consumed quota' })).toBeVisible()
    expect(screen.getByRole('group', { name: 'Refunded quota' })).toBeVisible()
    await act(() => i18n.changeLanguage('zh'))
    expect(screen.getByRole('group', { name: '消费额度' })).toBeVisible()
    expect(screen.getByRole('group', { name: '退款额度' })).toBeVisible()
  })
})
