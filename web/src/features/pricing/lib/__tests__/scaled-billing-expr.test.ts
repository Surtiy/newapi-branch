import { describe, expect, it } from 'vitest'

import {
  parseTiersFromExpr,
  splitBillingExprAndRequestRules,
  tryParseRequestRuleExpr,
} from '../billing-expr'
import { getDynamicPricingSummary } from '../dynamic-price'
import { evalExprLocally, tryParseVisualConfig } from '../tier-expr'

const base =
  'len <= 272000 ? tier("base", p * 5 + c * 30 + cr * 0.5) : tier("long_context", p * 10 + c * 45 + cr * 1)'
const rule = '(param("service_tier") == "priority" ? 2.5 : 1)'

describe('imported billing expression multipliers', () => {
  it.each([
    `(((${base}) * ${rule})) * 1.2`,
    `1.2*((${base})*${rule})`,
    `v1:(((${base}) * ${rule}) * 0.6) * 2`,
  ])(
    'fills scaled tier prices and preserves thresholds and priority rules for %s',
    (expression) => {
      const split = splitBillingExprAndRequestRules(expression)
      const tiers = parseTiersFromExpr(split.billingExpr)
      expect(tiers).toHaveLength(2)
      expect(tiers[0]).toMatchObject({
        inputPrice: 6,
        outputPrice: 36,
        cacheReadPrice: 0.6,
        conditions: [{ var: 'len', op: '<=', value: 272000 }],
      })
      expect(tiers[1]).toMatchObject({
        inputPrice: 12,
        outputPrice: 54,
        cacheReadPrice: 1.2,
        conditions: [],
      })
      expect(tryParseRequestRuleExpr(split.requestRuleExpr)).toMatchObject([
        {
          multiplier: '2.5',
          conditions: [{ path: 'service_tier', value: 'priority' }],
        },
      ])
      expect(tryParseVisualConfig(split.billingExpr)?.tiers[0]).toMatchObject({
        input_unit_cost: 6,
        output_unit_cost: 36,
        cache_read_unit_cost: 0.6,
      })
      const summary = getDynamicPricingSummary(
        {
          id: 1,
          model_name: 'gpt-5.5',
          quota_type: 0,
          model_ratio: 1,
          completion_ratio: 1,
          enable_groups: ['default'],
          billing_mode: 'tiered_expr',
          billing_expr: expression,
        },
        { tokenUnit: 'M' }
      )
      expect(summary?.isSpecialExpression).toBe(false)
      expect(summary?.hasRequestRules).toBe(true)
      expect(summary?.primaryEntries.map((entry) => entry.value)).toEqual([
        6, 36,
      ])
    }
  )

  it('keeps equivalent costs on both sides of the context threshold after folding a scale', () => {
    const split = splitBillingExprAndRequestRules(`(${base}) * 1.2`)
    for (const prompt of [1000, 272000, 272001]) {
      const extras = {
        cacheReadTokens: 100,
        cacheCreateTokens: 0,
        cacheCreate1hTokens: 0,
        imageTokens: 0,
        imageOutputTokens: 0,
        audioInputTokens: 0,
        audioOutputTokens: 0,
      }
      const before = evalExprLocally(`(${base}) * 1.2`, prompt, 100, extras)
      const after = evalExprLocally(split.billingExpr, prompt, 100, extras)
      expect(before.error).toBeNull()
      expect(after.error).toBeNull()
      expect(after.cost).toBeCloseTo(before.cost, 8)
      expect(after.matchedTier).toBe(before.matchedTier)
    }
  })

  it('recognizes zero as an explicit free price without deleting the request rule', () => {
    const split = splitBillingExprAndRequestRules(`((${base}) * ${rule}) * 0`)
    expect(parseTiersFromExpr(split.billingExpr)[0]).toMatchObject({
      inputPrice: 0,
      outputPrice: 0,
      cacheReadPrice: 0,
    })
    expect(tryParseRequestRuleExpr(split.requestRuleExpr)?.[0].multiplier).toBe(
      '2.5'
    )
  })

  it.each([
    `(${base}) * -1`,
    `(${base}) * 1e999`,
    `(${base}) * 1e308`,
    `(${base}) * unknown`,
    'tier("custom", max(p * 2 + c * 8, 100)) * 1.2',
    'param("premium") ? tier("pro", p * 4 + c * 16) : tier("base", p * 2 + c * 8) * 1.2',
    '(tier("base", p * 2 + c * 8) * 1.2',
    'tier("base", p * 2 + c * 8) *',
  ])(
    'preserves an unsupported or invalid expression unchanged: %s',
    (expression) => {
      expect(splitBillingExprAndRequestRules(expression)).toEqual({
        billingExpr: expression,
        requestRuleExpr: '',
      })
    }
  )
})
