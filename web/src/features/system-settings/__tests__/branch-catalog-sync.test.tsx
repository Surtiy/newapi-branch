import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { BranchCatalogSync } from '../models/branch-catalog-sync'

describe('main site catalog pagination', () => {
  it('updates an existing 1.2x price when syncing with a 1x multiplier', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          can_configure: true,
          channels: [
            {
              id: 11,
              name: 'image source',
              configured: true,
              bound_group: 'image-group',
            },
          ],
        },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          catalog_version: 'catalog',
          channel_version: 'channel',
          group_ratio: { 'image-group': 1 },
          models: [
            {
              model_name: 'image-model',
              model_type: 'image',
              imported: true,
              changed: true,
              local: {
                version: 'local',
                configured: { ModelPrice: 0.6 },
                effective: { ModelPrice: 0.6 },
              },
              incoming: { ModelPrice: 0.5 },
              groups: [
                {
                  name: 'image-group',
                  ratio: 1,
                  local: { ratio: 1, version: 'group' },
                  imported: true,
                },
              ],
            },
          ],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const user = userEvent.setup()
    const view = render(
      <QueryClientProvider client={client}>
        <BranchCatalogSync />
      </QueryClientProvider>
    )
    const fetch = await screen.findByRole('button', {
      name: 'Fetch main site models',
    })
    await waitFor(() => expect(fetch).toBeEnabled())
    await user.click(fetch)
    const update = await screen.findByRole('checkbox', {
      name: 'Update price for image-model',
    })
    expect(update).toHaveAttribute('aria-checked', 'true')
    await user.click(
      screen.getByRole('checkbox', { name: 'Select image-model' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Confirm selected models (1)' })
    )
    await user.click(await screen.findByRole('button', { name: 'Apply sync' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/option/branch_catalog/apply',
        expect.objectContaining({
          channel_id: 11,
          price_multiplier: 1,
          models: [
            expect.objectContaining({
              model_name: 'image-model',
              update_price: true,
              groups: ['image-group'],
            }),
          ],
        }),
        expect.anything()
      )
    )
    view.unmount()
    client.clear()
  })

  it('can add another group key without replacing the selected source', async () => {
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/option/branch_catalog/preview'
            ? { catalog_version: 'v', channel_version: 'v', models: [] }
            : { channel_id: 22, groups: ['video-group'] },
      },
    }))
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          can_configure: true,
          channels: [
            {
              id: 11,
              name: 'image source',
              configured: true,
              bound_group: 'image-group',
            },
          ],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const user = userEvent.setup()
    const view = render(
      <QueryClientProvider client={client}>
        <BranchCatalogSync />
      </QueryClientProvider>
    )
    await user.click(await screen.findByRole('button', { name: 'Add API key' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add API key' })
    const draft = within(dialog)
    expect(draft.queryByLabelText('Main site groups')).not.toBeInTheDocument()
    await user.type(draft.getByLabelText('Main site API key'), 'test-video-key')
    await user.click(draft.getByRole('button', { name: 'Save API key' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/option/branch_catalog/config',
        {
          channel_id: 0,
          api_key: 'test-video-key',
        },
        expect.anything()
      )
    )
    view.unmount()
    client.clear()
  })

  it('keeps blocked models with null prices visible when paging and expanding groups', async () => {
    const models = Array.from({ length: 20 }, (_, index) => ({
      model_name: `language-${index + 1}`,
      model_type: 'language',
      local: { version: 'empty', configured: {}, effective: {} },
      incoming: { ModelPrice: 1 },
      groups: [
        {
          name: 'video-group',
          ratio: 1,
          local: { ratio: 1, version: 'g' },
          imported: true,
        },
      ],
      imported: false,
      changed: true,
    }))
    const blocked = {
      model_name: 'zz-blocked-video',
      model_type: 'video',
      local: { version: 'empty', configured: {}, effective: {} },
      incoming: null,
      blocked: 'Video adapter is not enabled',
      groups: [
        {
          name: 'video-group',
          ratio: 1,
          local: { ratio: null, version: 'empty' },
          imported: false,
        },
      ],
      imported: false,
      changed: true,
    }
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          can_configure: false,
          channels: [
            {
              id: 1,
              name: 'Main site',
              configured: true,
              bound_group: 'video-group',
            },
          ],
        },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          models: [...models, blocked],
          group_ratio: {},
          catalog_version: 'catalog',
          channel_version: 'channel',
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const user = userEvent.setup()
    const view = render(
      <QueryClientProvider client={client}>
        <BranchCatalogSync />
      </QueryClientProvider>
    )
    const fetch = await screen.findByRole('button', {
      name: 'Fetch main site models',
    })
    await waitFor(() => expect(fetch).toBeEnabled())
    await user.click(fetch)
    await screen.findByText('language-1')
    await user.click(screen.getByRole('button', { name: 'Next' }))
    const row = await screen.findByRole('row', { name: /zz-blocked-video/ })
    expect(within(row).getByText('Video adapter is not enabled')).toBeVisible()
    expect(
      within(row).getByRole('checkbox', { name: 'Select zz-blocked-video' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(within(row).getAllByText('Unset price').length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(await screen.findByText('language-1')).toBeVisible()
    expect(
      post.mock.calls.every(
        ([path]) => path === '/api/option/branch_catalog/preview'
      )
    ).toBe(true)
    view.unmount()
    client.clear()
  })
})
