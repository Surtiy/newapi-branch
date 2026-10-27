import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { formatLogQuota } from '@/lib/format'

import type { MidjourneyLog } from '../../types'
import { useDrawingLogsColumns } from '../columns/drawing-logs-columns'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'

const legacyLog: MidjourneyLog = {
  id: 1,
  user_id: 2,
  channel_id: 17,
  code: 1,
  mj_id: 'mj-historical',
  action: 'IMAGINE',
  submit_time: 1788840000000,
  finish_time: 1788840003000,
  progress: '100%',
  prompt: '旧版提示词',
  prompt_en: 'An old Midjourney prompt',
  image_url: 'https://media.example/old-mj.png',
  status: 'SUCCESS',
}
const imageLog = {
  ...legacyLog,
  mj_id: 'img_request-42',
  model_name: 'image-model-v2',
  request_id: 'request-42',
  request_path: '/v1/images/generations',
  image_urls: ['/api/mj/42/image/0', '/api/mj/42/image/1'],
  request_params: { n: 2, size: '1024x1024', seed: 0, stream: false },
  quota: 12500,
}
const i18n = createInstance()
const animationsDescriptor = Object.getOwnPropertyDescriptor(
  Element.prototype,
  'getAnimations'
)

beforeEach(async () => {
  Object.defineProperty(Element.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
  let sequence = 0
  vi.stubGlobal(
    'URL',
    class extends URL {
      static createObjectURL = vi.fn(() => `blob:preview-${++sequence}`)
      static revokeObjectURL = vi.fn()
    }
  )
  vi.spyOn(api, 'get').mockResolvedValue({
    data: new Blob(['image'], { type: 'image/png' }),
  })
  await i18n.init({
    lng: 'en',
    resources: { en, zh },
    interpolation: { escapeValue: false },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  if (animationsDescriptor) {
    Object.defineProperty(
      Element.prototype,
      'getAnimations',
      animationsDescriptor
    )
  } else {
    Reflect.deleteProperty(Element.prototype, 'getAnimations')
  }
})

function DrawingTable(props: {
  log: MidjourneyLog
  isAdmin?: boolean
  mobile?: boolean
}) {
  const columns = useDrawingLogsColumns(props.isAdmin ?? false)
  const table = useReactTable({
    data: [props.log],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  if (props.mobile) {
    return <UsageLogsMobileList table={table} logCategory='drawing' />
  }
  return (
    <table>
      <thead>
        {table.getHeaderGroups().map((group) => (
          <tr key={group.id}>
            {group.headers.map((header) => (
              <th key={header.id}>
                {flexRender(
                  header.column.columnDef.header,
                  header.getContext()
                )}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id}>
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function renderLog(
  log: MidjourneyLog = imageLog,
  isAdmin = false,
  mobile = false
) {
  return render(
    <I18nextProvider i18n={i18n}>
      <DrawingTable log={log} isAdmin={isAdmin} mobile={mobile} />
    </I18nextProvider>
  )
}

test('multiple output images show their count and open each same-site URL, not the legacy thumbnail', async () => {
  renderLog()
  const user = userEvent.setup()
  expect(screen.getByRole('columnheader', { name: 'Model' })).toBeVisible()
  expect(screen.getByText('image-model-v2')).toBeVisible()
  expect(screen.getByText('Image count: 2')).toBeVisible()
  for (const [index, url] of imageLog.image_urls.entries()) {
    await user.click(screen.getByRole('button', { name: `Image ${index + 1}` }))
    const dialog = await screen.findByRole('dialog', { name: 'Image Preview' })
    const image = await within(dialog).findByRole('img', {
      name: 'Generated image',
    })
    expect(image).toHaveAttribute('src', `blob:preview-${index + 1}`)
    expect(api.get).toHaveBeenCalledWith(
      url,
      expect.objectContaining({ responseType: 'blob' })
    )
    fireEvent.load(image)
    expect(image).toHaveClass('opacity-100')
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
  }
})

test('a failed image preview can be closed and reopened with fresh loading state', async () => {
  renderLog()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Image 1' }))
  fireEvent.error(await screen.findByRole('img', { name: 'Generated image' }))
  expect(screen.getByText('Failed to load image')).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Image 1' }))
  expect(screen.queryByText('Failed to load image')).not.toBeInTheDocument()
  fireEvent.load(await screen.findByRole('img', { name: 'Generated image' }))
  expect(screen.getByRole('img', { name: 'Generated image' })).toHaveClass(
    'opacity-100'
  )
})

test.each([{ image_urls: undefined }, { image_urls: [] }])(
  'old MJ records with image_urls=$image_urls still preview image_url and translated prompts',
  async ({ image_urls }) => {
    renderLog({ ...legacyLog, image_urls })
    const user = userEvent.setup()
    const modelHeader = screen.getByRole('columnheader', { name: 'Model' })
    expect(modelHeader).toBeVisible()
    expect(screen.getByText('mj-historical')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'View' }))
    expect(
      screen.getByRole('img', { name: 'Generated image' })
    ).toHaveAttribute('src', legacyLog.image_url)
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await user.click(screen.getByRole('button', { name: legacyLog.prompt }))
    const dialog = await screen.findByRole('dialog', { name: 'Prompt Details' })
    expect(within(dialog).getByText(legacyLog.prompt_en ?? '')).toBeVisible()
  }
)

test('one new image uses the existing preview action and keeps its count', async () => {
  renderLog({ ...imageLog, image_urls: ['/api/mj/42/image/0'] })
  expect(screen.getByText('Image count: 1')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'View' }))
  expect(
    await screen.findByRole('img', { name: 'Generated image' })
  ).toHaveAttribute('src', 'blob:preview-1')
})

test('a row without images has no preview action but still offers details', () => {
  renderLog({ ...legacyLog, image_url: undefined })
  expect(screen.queryByRole('button', { name: 'View' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'View details' })).toBeVisible()
})

test.each([false, true])(
  'details expose saved prompts, parameters, cost and request identifiers with admin=%s',
  async (isAdmin) => {
    const user = userEvent.setup()
    renderLog(imageLog, isAdmin)
    expect(
      screen.queryByRole('columnheader', { name: 'Channel' }) !== null
    ).toBe(isAdmin)
    expect(
      screen.queryByRole('columnheader', { name: 'Submit Result' }) !== null
    ).toBe(isAdmin)
    const trigger = screen.getByRole('button', { name: 'View details' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', {
      name: 'Drawing Details',
    })
    expect(within(dialog).getByText(legacyLog.prompt)).toBeVisible()
    expect(within(dialog).getByText(legacyLog.prompt_en ?? '')).toBeVisible()
    expect(within(dialog).getByText('request-42')).toBeVisible()
    expect(within(dialog).getByText('/v1/images/generations')).toBeVisible()
    expect(within(dialog).getByText(formatLogQuota(12500))).toBeVisible()
    expect(dialog.querySelector('pre')?.textContent).toBe(
      JSON.stringify(imageLog.request_params, null, 2)
    )
    await user.click(
      within(dialog).getByRole('button', { name: 'Copy request parameters' })
    )
    expect(await navigator.clipboard.readText()).toBe(
      JSON.stringify(imageLog.request_params, null, 2)
    )
    expect(within(dialog).queryByText('Channel') !== null).toBe(isAdmin)
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
  }
)

test('historical details explain missing parameters and do not guess model, cost or request ID', async () => {
  renderLog({
    ...legacyLog,
    properties: '{"seed":123}',
    other: '{"model":"not-a-saved-model"}',
  })
  await userEvent.click(screen.getByRole('button', { name: 'View details' }))
  const dialog = await screen.findByRole('dialog', { name: 'Drawing Details' })
  expect(
    within(dialog).getByText('No saved request parameters for this task.')
  ).toBeVisible()
  expect(dialog).not.toHaveTextContent('not-a-saved-model')
  expect(dialog.querySelector('pre')).toBeNull()
  expect(
    within(dialog).queryByRole('button', { name: 'Copy request parameters' })
  ).not.toBeInTheDocument()
  for (const label of ['Model', 'Cost', 'Request ID']) {
    expect(within(dialog).getByText(label).parentElement).toHaveTextContent('-')
  }
})

test('an edit request with zero cost and empty saved parameters remains inspectable without a prompt', async () => {
  renderLog({
    ...imageLog,
    action: 'EDITS',
    prompt: '',
    prompt_en: '',
    quota: 0,
    request_params: {},
  })
  await userEvent.click(screen.getByRole('button', { name: 'View details' }))
  const dialog = await screen.findByRole('dialog', { name: 'Drawing Details' })
  expect(within(dialog).getByText(formatLogQuota(0))).toBeVisible()
  expect(dialog.querySelector('pre')).toHaveTextContent('{}')
})

test('drawing-specific labels follow the Chinese locale', async () => {
  await i18n.changeLanguage('zh')
  renderLog()
  expect(screen.getByText('图片数量：2')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: '查看详情' }))
  expect(await screen.findByRole('dialog', { name: '绘图详情' })).toBeVisible()
})

test('mobile drawing cards show the model, multiple previews and details action', async () => {
  renderLog(imageLog, false, true)
  expect(screen.getByText('image-model-v2')).toBeVisible()
  expect(screen.getByText('Image count: 2')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Image 2' }))
  expect(
    await screen.findByRole('img', { name: 'Generated image' })
  ).toHaveAttribute('src', 'blob:preview-1')
  await userEvent.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await userEvent.click(screen.getByRole('button', { name: 'View details' }))
  expect(
    await screen.findByRole('dialog', { name: 'Drawing Details' })
  ).toHaveTextContent('request-42')
})
