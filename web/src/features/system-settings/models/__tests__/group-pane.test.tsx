import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { BranchCatalogSync } from '../branch-catalog-sync'

afterEach(() => vi.restoreAllMocks())

function model(name: string, groups = ['image'], blocked?: string) {
  return {
    model_name: name,
    model_type: 'image',
    local: {
      version: 'local',
      configured: { ModelPrice: 0.6 },
      effective: { ModelPrice: 0.6 },
    },
    incoming: { ModelPrice: 0.5 },
    imported: true,
    changed: true,
    blocked,
    conflicting_groups: blocked ? groups : [],
    groups: groups.map((name) => ({
      name,
      ratio: 1,
      local: { ratio: 1, version: 'group' },
      imported: true,
    })),
  }
}

function renderSync(configured = true, canConfigure = true) {
  const config = {
    main_url: 'https://aicost.me',
    can_configure: canConfigure,
    channels: configured
      ? [
          {
            id: 7,
            name: 'AICost',
            configured: true,
            key_preview: 'sk-Abcd**********Wxyz',
            channel_version: 'source-v1',
            bound_groups: ['image', 'video'],
          },
        ]
      : ([] as Array<{
          id: number
          name: string
          configured: boolean
          key_preview?: string
          channel_version?: string
          bound_groups: string[]
        }>),
  }
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: config },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/option/branch_catalog/preview') {
      return {
        data: {
          success: true,
          data: {
            catalog_version: 'catalog',
            channel_version: 'channel',
            group_ratio: { image: 1, video: 1 },
            models: [
              model('image-model'),
              model('video-model', ['video']),
              model('shared-model', ['image', 'video']),
            ],
          },
        },
      }
    }
    return { data: { success: false, message: 'Key rejected' } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <BranchCatalogSync />
    </QueryClientProvider>
  )
  return { config, client, post }
}

it('opens add-key dialog without inline credential fields and clears secrets on cancel', async () => {
  const { client } = renderSync()
  await screen.findByText('image-model')
  expect(screen.queryByLabelText('Main site API key')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Add API key' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add API key' })
  const input = within(dialog).getByLabelText('Main site API key')
  expect(input).toHaveAttribute('type', 'password')
  fireEvent.change(input, { target: { value: 'draft-secret' } })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Add API key' }))
  expect(await screen.findByLabelText('Main site API key')).toHaveValue('')
  client.clear()
})

it('shows the server-masked credential in the sidebar without exposing a full key', async () => {
  const { client } = renderSync()
  await screen.findByText('image-model')
  const sidebar = within(
    screen.getByRole('complementary', { name: 'Main site groups' })
  )
  expect(sidebar.getByText('sk-Abcd**********Wxyz')).toBeVisible()
  expect(sidebar.queryByText('API key 1')).not.toBeInTheDocument()
  fireEvent.click(sidebar.getByRole('button', { name: 'Replace API key 1' }))
  expect(await screen.findByLabelText('Main site API key')).toHaveValue('')
  client.clear()
})

it('lists server-provided source groups without inferring extra groups in the UI', async () => {
  const { client, post } = renderSync()
  post.mockResolvedValue({
    data: {
      success: true,
      data: {
        catalog_version: 'directory',
        channel_version: 'directory',
        bound_groups: ['kiro', 'empty-bound-group'],
        group_ratio: { kiro: 0.11 },
        models: [model('kiro-model', ['kiro'])],
      },
    },
  })
  await screen.findByText('kiro-model')
  const sidebar = within(screen.getByRole('complementary'))
  expect(sidebar.getByText('kiro')).toBeVisible()
  expect(sidebar.getByText('empty-bound-group')).toBeVisible()
  expect(sidebar.queryByText('video')).not.toBeInTheDocument()
  client.clear()
})

it('warns about candidate groups without blocking a valid newly added source', async () => {
  const { client } = renderSync()
  expect(
    await screen.findByText('Review candidate directory groups')
  ).toBeVisible()
  const notice = screen
    .getByText('Review candidate directory groups')
    .closest('[role="note"]')
  expect(notice).not.toBeNull()
  expect(notice).toHaveTextContent('shared-model')
  expect(notice).toHaveTextContent('sk-Abcd**********Wxyz')
  expect(notice).toHaveTextContent('image / video')
  expect(notice).toHaveTextContent('does not identify actual token bindings')
  expect(await screen.findByText('image-model')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Fetch main site models' })
  ).toBeEnabled()
  client.clear()
})

it('adds only a credential and automatically displays its returned group and models', async () => {
  const { config, client, post } = renderSync(false)
  post.mockImplementation(async (url, body) => {
    if (url === '/api/option/branch_catalog/config') {
      expect(body).toEqual({ channel_id: 0, api_key: 'new-key' })
      config.channels = [
        {
          id: 8,
          name: 'AICost',
          configured: true,
          bound_groups: ['new-group'],
        },
      ]
      return {
        data: { success: true, data: { channel_id: 8, groups: ['new-group'] } },
      }
    }
    return {
      data: {
        success: true,
        data: {
          catalog_version: 'v',
          channel_version: 'v',
          models: [model('new-model', ['new-group'])],
        },
      },
    }
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Add API key' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add API key' })
  fireEvent.change(within(dialog).getByLabelText('Main site API key'), {
    target: { value: 'new-key' },
  })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save API key' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(
    await within(screen.getByRole('complementary')).findByText('new-group')
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /View group/ })
  ).not.toBeInTheDocument()
  expect(await screen.findByText('new-model')).toBeVisible()
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(screen.queryByLabelText('Main site API key')).not.toBeInTheDocument()
  client.clear()
})

it('retains the key after saving fails and clears the field error on edit', async () => {
  const { client } = renderSync(false)
  fireEvent.click(await screen.findByRole('button', { name: 'Add API key' }))
  const dialog = await screen.findByRole('dialog')
  const input = within(dialog).getByLabelText('Main site API key')
  fireEvent.change(input, { target: { value: 'bad-key' } })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save API key' }))
  expect(await within(dialog).findByText('Key rejected')).toBeVisible()
  expect(input).toHaveValue('bad-key')
  expect(input).toHaveAttribute('aria-invalid', 'true')
  fireEvent.change(input, { target: { value: 'corrected-key' } })
  await waitFor(() =>
    expect(within(dialog).queryByText('Key rejected')).not.toBeInTheDocument()
  )
  client.clear()
})

it('displays each conflicting group and its masked owner in the key dialog', async () => {
  const { client, post } = renderSync(false)
  const message =
    'Token sk-cccc**********3333 could not be saved:\nDuplicate groups: image, video; owner sk-aaaa**********1111 (source #7).\nDuplicate group: language; owner sk-bbbb**********2222 (source #8).'
  post.mockResolvedValue({ data: { success: false, message } })
  fireEvent.click(await screen.findByRole('button', { name: 'Add API key' }))
  const dialog = await screen.findByRole('dialog')
  fireEvent.change(within(dialog).getByLabelText('Main site API key'), {
    target: { value: 'new-draft' },
  })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save API key' }))
  const error = await within(dialog).findByText(/Token sk-cccc/)
  expect(error).toHaveTextContent('image, video')
  expect(error).toHaveTextContent('sk-aaaa**********1111')
  expect(error).toHaveTextContent('language')
  expect(error).toHaveTextContent('sk-bbbb**********2222')
  expect(error).toHaveClass('whitespace-pre-line')
  expect(within(dialog).getByLabelText('Main site API key')).toHaveValue(
    'new-draft'
  )
  client.clear()
})

it('lists all models without requiring a group selection', async () => {
  const { client } = renderSync()
  const pane = within(screen.getByRole('region', { name: 'Main site models' }))
  await pane.findByText('image-model')
  expect(await pane.findByText('video-model')).toBeVisible()
  fireEvent.click(screen.getByLabelText('Select image-model'))
  expect(
    screen.queryByRole('button', { name: /View group/ })
  ).not.toBeInTheDocument()
  expect(pane.getByText('image-model')).toBeVisible()
  expect(screen.getByLabelText('Select video-model')).not.toBeChecked()
  expect(
    screen.getByRole('button', { name: /Confirm selected models/ })
  ).toBeEnabled()
  client.clear()
})

it('does not treat multiple directory associations within one token as duplicate model names', async () => {
  const { client } = renderSync()
  await screen.findByText('image-model')
  expect(screen.queryByText('Duplicate model names')).not.toBeInTheDocument()
  fireEvent.click(screen.getByLabelText('Select filtered models'))
  expect(screen.getByLabelText('Select image-model')).toBeChecked()
  expect(screen.getByLabelText('Select shared-model')).toBeChecked()
  expect(screen.getByLabelText('Select shared-model')).not.toHaveAttribute(
    'aria-disabled',
    'true'
  )
  client.clear()
})

it('replaces a saved source through a modal without displaying the stored key', async () => {
  const { client, post } = renderSync()
  await screen.findByText('image-model')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Replace API key 1' }))
  const dialog = await screen.findByRole('dialog', { name: 'Replace API key' })
  const input = within(dialog).getByLabelText('Main site API key')
  expect(input).toHaveValue('')
  await user.type(input, 'replacement-key')
  await user.click(within(dialog).getByRole('button', { name: 'Save API key' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/option/branch_catalog/config',
      { channel_id: 7, api_key: 'replacement-key' },
      expect.anything()
    )
  )
  client.clear()
})

it('allows read-only operators to browse models without credential actions', async () => {
  const { client } = renderSync(true, false)
  await screen.findByText('image-model')
  expect(
    screen.queryByRole('button', { name: 'Add API key' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Replace API key 1' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Delete API key/ })
  ).not.toBeInTheDocument()
  expect(await screen.findByText('video-model')).toBeVisible()
  client.clear()
})

it('confirms the masked key and groups and does not delete on cancel', async () => {
  const { client, post } = renderSync()
  await screen.findByText('image-model')
  fireEvent.click(screen.getByRole('button', { name: 'Delete API key 1' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Delete API key',
  })
  expect(dialog).toHaveTextContent('sk-Abcd**********Wxyz')
  expect(dialog).toHaveTextContent('image, video')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(
    post.mock.calls.some(([url]) => url === '/api/option/branch_catalog/delete')
  ).toBe(false)
  expect(
    within(screen.getByRole('complementary')).getByText('image')
  ).toBeVisible()
  client.clear()
})

it('deletes a source, retains all remaining models, then clears the last source', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    bound_groups: ['audio'],
    channel_version: 'source-v2',
  })
  post.mockImplementation(async (url, body) => {
    const request = body as { channel_id: number; channel_version?: string }
    if (url === '/api/option/branch_catalog/delete') {
      expect(request.channel_version).toBe(
        request.channel_id === 7 ? 'source-v1' : 'source-v2'
      )
      config.channels = config.channels.filter(
        (channel) => channel.id !== request.channel_id
      )
      return { data: { success: true, data: { disabled_channels: 2 } } }
    }
    return {
      data: {
        success: true,
        data: {
          models: [
            model(
              request.channel_id === 7 ? 'image-model' : 'audio-model',
              request.channel_id === 7 ? ['image'] : ['audio']
            ),
          ],
        },
      },
    }
  })
  await screen.findByText('image-model')
  for (const expected of ['audio-model', 'No main site models']) {
    fireEvent.click(screen.getByRole('button', { name: 'Delete API key 1' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    await waitFor(() => expect(screen.getByText(expected)).toBeVisible())
  }
  expect(
    screen.queryByRole('button', { name: /View group/ })
  ).not.toBeInTheDocument()
  expect(screen.queryByText('image-model')).not.toBeInTheDocument()
  client.clear()
})

it('keeps the source and shows an inline error when deletion fails', async () => {
  const { client, post } = renderSync()
  await screen.findByText('image-model')
  post.mockResolvedValue({
    data: { success: false, message: 'Source configuration changed' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Delete API key 1' }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'Source configuration changed'
  )
  expect(within(dialog).getByRole('button', { name: 'Delete' })).toBeEnabled()
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(
    within(screen.getByRole('complementary')).getByText('image')
  ).toBeInTheDocument()
  client.clear()
})

it('disables confirmation and cancel while deletion is pending', async () => {
  const { config, client, post } = renderSync()
  await screen.findByText('image-model')
  let resolve!: (value: {
    data: { success: boolean; data: { disabled_channels: number } }
  }) => void
  post.mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Delete API key 1' }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  await waitFor(() =>
    expect(
      within(dialog).getByRole('button', { name: 'Delete' })
    ).toBeDisabled()
  )
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled()
  config.channels = []
  resolve({ data: { success: true, data: { disabled_channels: 0 } } })
  await screen.findByText('No main site models')
  client.clear()
})

it('shows a recoverable error instead of crashing when the model catalog is malformed', async () => {
  const { client, post } = renderSync()
  post.mockResolvedValue({ data: { success: true, data: { channel_id: 7 } } })
  expect(
    await screen.findByText(/Invalid main site model catalog/)
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Add API key' })).toBeEnabled()
  client.clear()
})

it('lists every group and model from every saved key without group selection', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second source',
    configured: true,
    bound_groups: ['language', 'audio'],
  })
  post.mockImplementation(async (_url, body) => {
    const channelId = (body as { channel_id: number }).channel_id
    const rows =
      channelId === 8
        ? [
            model('language-model', ['language']),
            model('audio-model', ['audio']),
          ]
        : [model('image-model'), model('video-model', ['video'])]
    return {
      data: {
        success: true,
        data: { catalog_version: 'v', channel_version: 'v', models: rows },
      },
    }
  })
  await screen.findByText('image-model')
  const sidebar = within(
    screen.getByRole('complementary', { name: 'Main site groups' })
  )
  for (const name of ['image', 'video', 'language', 'audio']) {
    expect(sidebar.getByText(name)).toBeVisible()
  }
  expect(await screen.findByText('language-model')).toBeVisible()
  expect(screen.getByText('image-model')).toBeVisible()
  expect(screen.getByText('audio-model')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /View group/ })
  ).not.toBeInTheDocument()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/option/branch_catalog/preview',
      { channel_id: 8 },
      expect.anything()
    )
  )
  expect(sidebar.getByText('image')).toBeVisible()
  client.clear()
})

it('allows identical model names in distinct groups to be selected together without warnings', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second source',
    configured: true,
    key_preview: 'sk-Efgh**********1234',
    bound_groups: ['other'],
    channel_version: 'source-v2',
  })
  post.mockImplementation(async (_url, body) => {
    const channelId = (body as { channel_id: number }).channel_id
    return {
      data: {
        success: true,
        data: {
          catalog_version: 'v',
          channel_version: 'v',
          group_ratio: channelId === 7 ? { image: 1 } : { other: 2 },
          models: [
            model('same-model', channelId === 7 ? ['image'] : ['other']),
          ],
        },
      },
    }
  })
  await waitFor(() =>
    expect(screen.getAllByLabelText('Select same-model')).toHaveLength(2)
  )
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  const choices = screen.getAllByLabelText('Select same-model')
  fireEvent.click(choices[0])
  fireEvent.click(choices[1])
  expect(choices[0]).toBeChecked()
  expect(choices[1]).toBeChecked()
  fireEvent.click(screen.getByLabelText('Select filtered models'))
  expect(choices[0]).not.toBeChecked()
  expect(choices[1]).not.toBeChecked()
  fireEvent.click(screen.getByLabelText('Select filtered models'))
  expect(choices[0]).toBeChecked()
  expect(choices[1]).toBeChecked()
  client.clear()
})

it('shows same-name models from a later token beside the first token on the first page', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    key_preview: 'sk-Efgh**********1234',
    bound_groups: ['other'],
  })
  post.mockImplementation(async (_url, body) => {
    const channelId = (body as { channel_id: number }).channel_id
    return {
      data: {
        success: true,
        data: {
          catalog_version: 'v',
          channel_version: 'v',
          models:
            channelId === 7
              ? [
                  model('claude-opus-4-6'),
                  ...Array.from({ length: 25 }, (_, index) =>
                    model(`wan-${index + 1}`)
                  ),
                ]
              : [model('claude-opus-4-6', ['other'])],
        },
      },
    }
  })
  await waitFor(() =>
    expect(screen.getAllByLabelText('Select claude-opus-4-6')).toHaveLength(2)
  )
  const rows = screen.getAllByRole('row', { name: /claude-opus-4-6/ })
  expect(within(rows[0]).getByText('image')).toBeVisible()
  expect(within(rows[1]).getByText('other')).toBeVisible()
  expect(within(rows[0]).getByText('sk-Abcd**********Wxyz')).toBeVisible()
  expect(within(rows[1]).getByText('sk-Efgh**********1234')).toBeVisible()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  client.clear()
})

it('keeps all sources of the last model together at a page boundary and retains their selection', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    bound_groups: ['other'],
  })
  post.mockImplementation(async (_url, body) => ({
    data: {
      success: true,
      data: {
        catalog_version: 'v',
        channel_version: 'v',
        models:
          (body as { channel_id: number }).channel_id === 7
            ? Array.from({ length: 21 }, (_, index) =>
                model(`model-${index + 1}`)
              )
            : [model('model-20', ['other'])],
      },
    },
  }))
  await waitFor(() =>
    expect(screen.getAllByLabelText('Select model-20')).toHaveLength(2)
  )
  for (const choice of screen.getAllByLabelText('Select model-20')) {
    fireEvent.click(choice)
  }
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  expect(await screen.findByText('model-21')).toBeVisible()
  expect(screen.queryByText('model-20')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
  for (const choice of await screen.findAllByLabelText('Select model-20')) {
    expect(choice).toBeChecked()
  }
  client.clear()
})

it('warns only for overlapping groups, lists masked owners, and selects one owner per group', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    key_preview: 'sk-Efgh**********1234',
    bound_groups: ['image', 'other'],
  })
  post.mockImplementation(async (_url, body) => ({
    data: {
      success: true,
      data: {
        catalog_version: 'v',
        channel_version: 'v',
        models: [
          model(
            'same-model',
            (body as { channel_id: number }).channel_id === 7
              ? ['image', 'video']
              : ['image', 'other']
          ),
        ],
      },
    },
  }))
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('Duplicate model names in the same group')
  expect(alert).toHaveTextContent('sk-Abcd**********Wxyz')
  expect(alert).toHaveTextContent('sk-Efgh**********1234')
  expect(alert).toHaveTextContent('image')
  expect(alert).not.toHaveTextContent('video')
  expect(alert).not.toHaveTextContent('other')
  const choices = screen.getAllByLabelText('Select same-model')
  fireEvent.click(choices[0])
  fireEvent.click(choices[1])
  expect(choices[0]).not.toBeChecked()
  expect(choices[1]).toBeChecked()
  client.clear()
})

it.each([false, true])(
  'syncs both groups of the same model while protecting concurrent price changes (concurrent=%s)',
  async (concurrent) => {
    const { config, client, post } = renderSync()
    config.channels.push({
      id: 8,
      name: 'Second',
      configured: true,
      bound_groups: ['other'],
    })
    let applied = 0
    post.mockImplementation(async (url, body) => {
      const payload = body as {
        channel_id: number
        models?: Array<{ expected_version: string; groups: string[] }>
      }
      if (url.endsWith('/apply')) {
        expect(payload.models?.[0].expected_version).toBe(
          applied ? 'committed' : 'local'
        )
        expect(payload.models?.[0].groups).toEqual(
          payload.channel_id === 7 ? ['image'] : ['other']
        )
        applied++
        return {
          data: {
            success: true,
            data: { count: 1, pricing_versions: { 'same-model': 'committed' } },
          },
        }
      }
      const row = model(
        'same-model',
        payload.channel_id === 7 ? ['image'] : ['other']
      )
      if (applied) {
        row.local.version = concurrent ? 'external-edit' : 'committed'
      }
      return {
        data: {
          success: true,
          data: { catalog_version: 'v', channel_version: 'v', models: [row] },
        },
      }
    })
    await waitFor(() =>
      expect(screen.getAllByLabelText('Select same-model')).toHaveLength(2)
    )
    fireEvent.click(screen.getByLabelText('Select filtered models'))
    fireEvent.click(
      screen.getByRole('button', { name: 'Confirm selected models (2)' })
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Apply sync' }))
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Apply sync' })
      ).not.toBeInTheDocument()
    )
    await waitFor(() => expect(applied).toBe(concurrent ? 1 : 2))
    client.clear()
  }
)

it('replaces the entire snapshot on refresh, clears selection, and hides stale data on failed fetch', async () => {
  const { client, post } = renderSync()
  await screen.findByText('image-model')
  fireEvent.click(screen.getByLabelText('Select image-model'))
  const fetch = screen.getByRole('button', { name: 'Fetch main site models' })
  post.mockResolvedValue({
    data: {
      success: true,
      data: {
        catalog_version: 'new',
        channel_version: 'new',
        group_ratio: { added: 1 },
        models: [model('added-model', ['added'])],
      },
    },
  })
  fireEvent.click(fetch)
  expect(await screen.findByText('added-model')).toBeVisible()
  expect(screen.queryByText('image-model')).not.toBeInTheDocument()
  expect(screen.queryByText('video-model')).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Confirm selected models (0)' })
  ).toBeDisabled()
  expect(
    within(screen.getByRole('complementary')).getByText('added')
  ).toBeVisible()
  await waitFor(() => expect(fetch).toBeEnabled())
  post.mockResolvedValue({
    data: { success: false, message: 'Main site temporarily unavailable' },
  })
  fireEvent.click(fetch)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Main site temporarily unavailable'
  )
  expect(screen.queryByText('added-model')).not.toBeInTheDocument()
  await waitFor(() => expect(fetch).toBeEnabled())
  post.mockResolvedValue({
    data: {
      success: true,
      data: {
        catalog_version: 'empty',
        channel_version: 'empty',
        group_ratio: {},
        models: [],
      },
    },
  })
  fireEvent.click(fetch)
  expect(await screen.findByText('No matching models')).toBeVisible()
  expect(screen.queryByText('added-model')).not.toBeInTheDocument()
  client.clear()
})

it('syncs all selected sources sequentially and refreshes shared group versions between batches', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    bound_groups: ['image'],
    channel_version: 'source-v2',
  })
  let version = 'original-group'
  post.mockImplementation(async (url, body) => {
    const payload = body as {
      channel_id: number
      groups?: Array<{ expected_version: string }>
    }
    if (url.endsWith('/apply')) {
      expect(payload.groups?.[0].expected_version).toBe(version)
      version = 'updated-group'
      return { data: { success: true, data: { count: 1 } } }
    }
    const row = model(payload.channel_id === 7 ? 'first-model' : 'second-model')
    row.groups[0].local.version = version
    return {
      data: {
        success: true,
        data: {
          catalog_version: 'v',
          channel_version: 'v',
          group_ratio: { image: 1 },
          models: [row],
        },
      },
    }
  })
  await screen.findByText('second-model')
  fireEvent.click(screen.getByLabelText('Select filtered models'))
  fireEvent.click(
    screen.getByRole('button', { name: 'Confirm selected models (2)' })
  )
  fireEvent.click(await screen.findByRole('button', { name: 'Apply sync' }))
  await waitFor(() =>
    expect(
      post.mock.calls.filter(([path]) => path.endsWith('/apply'))
    ).toHaveLength(2)
  )
  client.clear()
})

it('uses each newly entered multiplier across consecutive syncs of both same-name sources', async () => {
  const { config, client, post } = renderSync()
  config.channels.push({
    id: 8,
    name: 'Second',
    configured: true,
    key_preview: 'sk-Efgh**********1234',
    bound_groups: ['other'],
    channel_version: 'source-v2',
  })
  let localPrice = 0.6
  let version = 'initial'
  const multipliers = [1.37, 0.63, 2.05, 0.11, 3.7, 1, 1]
  const applied: Array<{ channel: number; multiplier: number; price: number }> =
    []
  post.mockImplementation(async (url, body) => {
    const payload = body as {
      channel_id: number
      price_multiplier?: number
      models?: Array<{ expected_version: string; groups: string[] }>
    }
    if (url.endsWith('/apply')) {
      expect(payload.models?.[0].expected_version).toBe(version)
      expect(payload.models?.[0].groups).toEqual(
        payload.channel_id === 7 ? ['image'] : ['other']
      )
      const multiplier = payload.price_multiplier
      expect(multiplier).toBe(multipliers[Math.floor(applied.length / 2)])
      if (typeof multiplier !== 'number') throw new Error('Missing multiplier')
      localPrice = 0.5 * multiplier
      version = `committed-${applied.length + 1}`
      applied.push({
        channel: payload.channel_id,
        multiplier,
        price: localPrice,
      })
      return {
        data: {
          success: true,
          data: { count: 1, pricing_versions: { 'same-model': version } },
        },
      }
    }
    const row = model(
      'same-model',
      payload.channel_id === 7 ? ['image'] : ['other']
    )
    row.local = {
      version,
      configured: { ModelPrice: localPrice },
      effective: { ModelPrice: localPrice },
    }
    return {
      data: {
        success: true,
        data: {
          catalog_version: 'catalog',
          channel_version: 'channel',
          models: [row],
        },
      },
    }
  })
  try {
    for (const [round, multiplier] of multipliers.entries()) {
      await waitFor(() =>
        expect(screen.getAllByLabelText('Select same-model')).toHaveLength(2)
      )
      await waitFor(() =>
        expect(
          screen.getByRole('button', { name: 'Fetch main site models' })
        ).toBeEnabled()
      )
      fireEvent.change(screen.getByLabelText('Unified price multiplier'), {
        target: { value: String(multiplier) },
      })
      fireEvent.click(screen.getByLabelText('Select filtered models'))
      expect(
        screen
          .getAllByLabelText('Select same-model')
          .every((choice) => choice.getAttribute('aria-checked') === 'true')
      ).toBe(true)
      fireEvent.click(
        screen.getByRole('button', { name: 'Confirm selected models (2)' })
      )
      expect(await screen.findByRole('alertdialog')).toHaveTextContent(
        `${multiplier} multiplier`
      )
      fireEvent.click(await screen.findByRole('button', { name: 'Apply sync' }))
      await waitFor(() => expect(applied).toHaveLength((round + 1) * 2))
      await waitFor(() =>
        expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
      )
      await waitFor(() =>
        expect(
          screen.getByRole('button', { name: 'Confirm selected models (0)' })
        ).toBeDisabled()
      )
      expect(localPrice).toBeCloseTo(0.5 * multiplier, 12)
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    }
    expect(applied.map((entry) => entry.channel)).toEqual(
      multipliers.flatMap(() => [7, 8])
    )
    expect(localPrice).toBe(0.5)
  } finally {
    client.clear()
  }
})
