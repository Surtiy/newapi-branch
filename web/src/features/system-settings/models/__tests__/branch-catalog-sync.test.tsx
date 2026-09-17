import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { BranchCatalogSync } from '../branch-catalog-sync'

afterEach(() => vi.restoreAllMocks())

function renderSync(configured = false, canConfigure = true) {
  const config = {
    main_url: 'https://aicost.me',
    can_configure: canConfigure,
    channels: configured ? [{ id: 7, name: 'AICost', configured: true }] : [],
  }
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: config },
  }))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <BranchCatalogSync />
    </QueryClientProvider>
  )
  return { config, client }
}

it('creates configuration from an API key, clears the key and enables fetching', async () => {
  const { config, client } = renderSync()
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, body) => {
    expect(body).toEqual({ channel_id: 0, api_key: 'test-key' })
    config.channels = [{ id: 7, name: 'AICost', configured: true }]
    return { data: { success: true, data: { channel_id: 7 } } }
  })
  const save = await screen.findByRole('button', { name: 'Save API key' })
  expect(screen.getByText('https://aicost.me')).toBeVisible()
  expect(save).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Fetch main site models' })
  ).toBeDisabled()
  const input = screen.getByLabelText('Main site API key')
  expect(input).toHaveAttribute('type', 'password')
  fireEvent.change(input, { target: { value: 'test-key' } })
  fireEvent.click(save)
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Fetch main site models' })
    ).toBeEnabled()
  )
  expect(input).toHaveValue('')
  expect(post).toHaveBeenCalledWith(
    '/api/option/branch_catalog/config',
    expect.anything(),
    expect.anything()
  )
  client.clear()
})

it('keeps the existing configuration usable and blocks fetching while a replacement key is unsaved', async () => {
  const { client } = renderSync(true)
  const fetch = screen.getByRole('button', { name: 'Fetch main site models' })
  await waitFor(() => expect(fetch).toBeEnabled())
  const input = screen.getByLabelText('Main site API key')
  expect(input).toHaveValue('')
  fireEvent.change(input, { target: { value: 'replacement' } })
  expect(fetch).toBeDisabled()
  fireEvent.change(input, { target: { value: '' } })
  expect(fetch).toBeEnabled()
  client.clear()
})

it('does not allow operators without sensitive channel permission to edit credentials', async () => {
  const { client } = renderSync(true, false)
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Fetch main site models' })
    ).toBeEnabled()
  )
  expect(screen.getByLabelText('Main site API key')).toBeDisabled()
  expect(
    screen.queryByRole('button', { name: 'Save API key' })
  ).not.toBeInTheDocument()
  client.clear()
})

it('keeps the entered key available to correct when upstream validation fails', async () => {
  const { client } = renderSync()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Key rejected' },
  })
  const save = await screen.findByRole('button', { name: 'Save API key' })
  fireEvent.change(screen.getByLabelText('Main site API key'), {
    target: { value: 'invalid-key' },
  })
  fireEvent.click(save)
  await waitFor(() => expect(save).toBeEnabled())
  expect(screen.getByLabelText('Main site API key')).toHaveValue('invalid-key')
  expect(
    screen.getByRole('button', { name: 'Fetch main site models' })
  ).toBeDisabled()
  client.clear()
})
