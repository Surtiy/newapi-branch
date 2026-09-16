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
import { describe, expect, it } from 'vitest'

import { buildPricingChanges, type ModelPricingConfig } from '../api'
import {
  applyPriceSyncSelections,
  applyPricingDraft,
  pricingFromDraft,
  pricingOptions,
  pricingRow,
} from '../pricing'
import { parseSimpleUnitPricingExpression } from '@/features/system-settings/models/model-pricing-snapshots'

describe('shared model pricing', () => {
  it('preserves explicit zero prices and cache-write configuration', () => {
    expect(
      pricingFromDraft({
        name: 'example',
        billingMode: 'per-token',
        ratio: '0',
        completionRatio: '2',
        cacheRatio: '0',
        createCacheRatio: '1.25',
      })
    ).toEqual({
      'billing_setting.billing_mode': 'ratio',
      ModelRatio: 0,
      CompletionRatio: 2,
      CacheRatio: 0,
      CreateCacheRatio: 1.25,
    })
    expect(
      pricingFromDraft({
        name: 'example',
        billingMode: 'per-request',
        price: '',
      })
    ).not.toHaveProperty('ModelPrice')
    expect(
      pricingFromDraft({
        name: 'example',
        billingMode: 'per-request',
        price: '0',
      })
    ).toHaveProperty('ModelPrice', 0)
  })

  it('keeps token and task expressions intact through both editing and sync', () => {
    for (const expression of [
      'len <= 200000 ? tier("short", p * 2 + cr * 0.2 + cc * 2.5) : tier("long", p * 4)',
      'tier("base", u("seconds") * 0.4)',
    ]) {
      const values = {
        'billing_setting.billing_mode': 'tiered_expr',
        'billing_setting.billing_expr': expression,
        ModelRatio: 1,
      }
      expect(pricingFromDraft(pricingRow('example', values))).toEqual(values)
    }
  })

  it('does not persist another model’s built-in display expression when editing one price', () => {
    const options = pricingOptions({
      ModelPrice: '{"edited":1}',
      BillingMode: '{"builtin":"tiered_expr"}',
      BillingExpr: '{"builtin":"tier(\\"base\\", p * 2)"}',
    })
    const snapshot: ModelPricingConfig = {
      options,
      empty_version: 'empty',
      entries: [
        {
          model_name: 'edited',
          version: 'v1',
          configured: { ModelPrice: 1 },
          effective: { ModelPrice: 1 },
        },
        {
          model_name: 'builtin',
          version: 'empty',
          configured: {},
          effective: {
            'billing_setting.billing_mode': 'tiered_expr',
            'billing_setting.billing_expr': 'tier("base", p * 2)',
          },
        },
      ],
    }
    const after = applyPricingDraft(options, {
      name: 'edited',
      billingMode: 'per-request',
      price: '2',
    })
    expect(buildPricingChanges(snapshot, options, after)).toEqual([
      {
        model_name: 'edited',
        expected_version: 'v1',
        pricing: { ModelPrice: 2, 'billing_setting.billing_mode': 'ratio' },
      },
    ])
  })

  it('clears conflicting expression settings when a fixed price is selected for sync', () => {
    const options = pricingOptions({
      ModelRatio: '{"example":1}',
      CreateCacheRatio: '{"example":1.25}',
      BillingMode: '{"example":"tiered_expr"}',
      BillingExpr: '{"example":"tier(\\"base\\", p * 2)"}',
    })
    const after = applyPriceSyncSelections(options, {
      example: { model_price: 0 },
    })
    expect(JSON.parse(after.ModelPrice)).toEqual({ example: 0 })
    expect(JSON.parse(after.ModelRatio)).toEqual({})
    expect(JSON.parse(after.CreateCacheRatio)).toEqual({})
    expect(JSON.parse(after['billing_setting.billing_expr'])).toEqual({})
    expect(JSON.parse(after['billing_setting.billing_mode'])).toEqual({
      example: 'ratio',
    })
  })

  it('rejects invalid prices instead of silently coercing them', () => {
    for (const price of ['-1', 'NaN', 'Infinity', 'invalid']) {
      expect(() =>
        pricingFromDraft({ name: 'example', billingMode: 'per-request', price })
      ).toThrow()
    }
  })

  it('represents a fixed per-second price with the internal task expression', () => {
    expect(
      pricingFromDraft({
        name: 'video',
        billingMode: 'per-second',
        price: '0.4',
      })
    ).toEqual({
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr':
        'tier("second", u("seconds") * 0.4)',
    })
  })

  it('replaces stale request pricing when switching a model to per-second', () => {
    const options = pricingOptions({
      ModelPrice: '{"video":1.2}',
      ModelRatio: '{"video":3}',
      BillingMode: '{"video":"ratio"}',
      BillingExpr: '{}',
    })

    const after = applyPricingDraft(options, {
      name: 'video',
      billingMode: 'per-second',
      price: '0.4',
    })

    expect(JSON.parse(after.ModelPrice)).toEqual({})
    expect(JSON.parse(after.ModelRatio)).toEqual({})
    expect(JSON.parse(after['billing_setting.billing_mode'])).toEqual({
      video: 'tiered_expr',
    })
    expect(JSON.parse(after['billing_setting.billing_expr'])).toEqual({
      video: 'tier("second", u("seconds") * 0.4)',
    })
  })

  it('replaces stale second pricing when switching a model to per-request', () => {
    const options = pricingOptions({
      ModelPrice: '{}',
      ModelRatio: '{}',
      BillingMode: '{"video":"tiered_expr"}',
      BillingExpr: '{"video":"tier(\\"second\\", u(\\"seconds\\") * 0.4)"}',
    })

    const after = applyPricingDraft(options, {
      name: 'video',
      billingMode: 'per-request',
      price: '1.2',
    })

    expect(JSON.parse(after.ModelPrice)).toEqual({ video: 1.2 })
    expect(JSON.parse(after.ModelRatio)).toEqual({})
    expect(JSON.parse(after['billing_setting.billing_expr'])).toEqual({})
    expect(JSON.parse(after['billing_setting.billing_mode'])).toEqual({
      video: 'ratio',
    })
  })

  it('recognizes simple request and second expressions for the operator UI', () => {
    expect(parseSimpleUnitPricingExpression('tier("request", 1.05)')).toEqual({
      unit: 'request',
      price: '1.05',
    })
    expect(
      parseSimpleUnitPricingExpression(
        'tier("second", u("seconds") * 0.4)'
      )
    ).toEqual({ unit: 'second', price: '0.4' })
    expect(
      pricingRow('video', {
        'billing_setting.billing_mode': 'tiered_expr',
        'billing_setting.billing_expr':
          'tier("second", u("seconds") * 0.4)',
      }).billingMode
    ).toBe('per-second')
  })
})
