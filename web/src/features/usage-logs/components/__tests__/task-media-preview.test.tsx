import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  renderHook,
  screen,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'

import type { TaskArtifactProjection, TaskLog } from '../../types'
import { useTaskLogsColumns } from '../columns/task-logs-columns'
import { TaskDetailsDialog } from '../dialogs/task-details-dialog'
import { TaskArtifactsCell } from '../task-artifacts'
import { TaskDetailsCell } from '../task-details-cell'

// Provider brand icons are unrelated to task row actions or media playback.
vi.mock('@lobehub/icons', () => ({}))

const task: TaskLog = {
  id: 1,
  user_id: 1,
  platform: 'aicost-branch',
  task_id: 'task-public',
  action: 'GENERATE',
  channel_id: 17,
  group: 'default',
  quota: 400000,
  submit_time: 1,
  status: 'SUCCESS',
}
const mediaUrl = `https://gateway.example:3080/v1/tasks/task-public/artifacts/video/content?access=${'A'.repeat(43)}`
const video: TaskArtifactProjection = {
  artifacts: [
    {
      key: 'video',
      type: 'video',
      mime_type: 'video/mp4',
      content_url: mediaUrl,
    },
  ],
}
const i18n = createInstance()
let client: QueryClient

beforeEach(async () => {
  await i18n.init({
    lng: 'en',
    resources: { en },
    interpolation: { escapeValue: false },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => client.clear())

function renderDetails(log = task, projection = video, isAdmin = false) {
  client.setQueryData(['usage-logs', 'task-artifacts', log.task_id], projection)
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <TaskDetailsDialog
          log={log}
          isAdmin={isAdmin}
          isRoot={false}
          open
          onOpenChange={() => undefined}
        />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

test.each([false, true])(
  'task columns keep details without a duplicate artifacts column for admin=%s',
  (isAdmin) => {
    const { result } = renderHook(() => useTaskLogsColumns(isAdmin, false), {
      wrapper: ({ children }) => (
        <I18nextProvider i18n={i18n}>{children}</I18nextProvider>
      ),
    })
    expect(result.current.some((column) => column.id === 'artifacts')).toBe(
      false
    )
    expect(result.current.some((column) => column.header === 'Artifacts')).toBe(
      false
    )
    expect(
      result.current.find((column) => column.header === 'Details')
    ).toMatchObject({
      accessorKey: 'fail_reason',
      cell: expect.any(Function),
    })
  }
)

test.each([false, true])(
  'task details play projected video for admin=%s',
  async (isAdmin) => {
    renderDetails(task, video, isAdmin)
    const dialog = await screen.findByRole('dialog')
    const player = dialog.querySelector('video')
    expect(player).toHaveAttribute('src', mediaUrl)
    expect(player).toHaveAttribute('controls')
    expect(player).toHaveAttribute('playsinline')
    expect(
      within(dialog).getByRole('button', { name: 'Download' })
    ).toHaveAttribute('href', mediaUrl)
  }
)

test('task details preserve the signed legacy video fallback', async () => {
  renderDetails(
    { ...task, legacy_video_available: true },
    { artifacts: [], legacyContentUrl: mediaUrl }
  )
  expect(
    (await screen.findByRole('dialog')).querySelector('video')
  ).toHaveAttribute('src', mediaUrl)
})

test.each(['FAILURE', 'IN_PROGRESS'])(
  'task details do not load media for %s tasks',
  async (status) => {
    renderDetails({ ...task, status })
    expect(
      (await screen.findByRole('dialog')).querySelector('video')
    ).toBeNull()
    expect(
      screen.queryByRole('region', { name: 'Preview' })
    ).not.toBeInTheDocument()
  }
)

test('empty artifacts do not fabricate a video URL', async () => {
  renderDetails(task, { artifacts: [] })
  expect((await screen.findByRole('dialog')).querySelector('video')).toBeNull()
  expect(screen.getByText('None')).toBeVisible()
})

test('failed media can be retried from task details', async () => {
  renderDetails()
  const dialog = await screen.findByRole('dialog')
  const player = dialog.querySelector('video')
  if (!player) throw new Error('Task details must contain the video player')
  fireEvent.error(player)
  expect(
    screen.getByText('Media preview failed. Please try again.')
  ).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(dialog.querySelector('video')).toHaveAttribute('src', mediaUrl)
  expect(dialog.querySelector('video')).not.toBe(player)
})

test('artifact action uses the same player and public download URL', async () => {
  client.setQueryData(['usage-logs', 'task-artifacts', task.task_id], video)
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <TaskArtifactsCell log={task} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  fireEvent.click(screen.getByRole('button', { name: 'Artifacts' }))
  const dialog = await screen.findByRole('dialog')
  expect(dialog.querySelector('video')).toHaveAttribute('src', mediaUrl)
  expect(
    within(dialog).getByRole('button', { name: 'Download' })
  ).toHaveAttribute('href', mediaUrl)
})

test('task details preview every image and video in a mixed result', async () => {
  const imageUrl = mediaUrl.replace('/video/', '/image-1/')
  renderDetails(task, {
    artifacts: [
      {
        key: 'image-1',
        type: 'image',
        mime_type: 'image/png',
        content_url: imageUrl,
      },
      ...video.artifacts,
    ],
  })
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByRole('img', { name: 'image-1' })).toHaveAttribute(
    'src',
    imageUrl
  )
  expect(dialog.querySelector('video')).toHaveAttribute('src', mediaUrl)
  expect(
    within(dialog).getAllByRole('button', { name: 'Download' })
  ).toHaveLength(2)
})

test('task parameters show the saved prompt, image references and explicit zero values', async () => {
  const params = {
    model: 'public-video',
    prompt: 'A quiet street, camera moves slowly.\nKeep the original lighting.',
    image_urls: [
      'https://media.example/first.png',
      'https://media.example/last.png',
    ],
    seconds: 8,
    seed: 0,
    face: { enabled: false },
  }
  renderDetails({ ...task, request_params: params })
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Request Parameters' }))
  const panel = screen.getByRole('tabpanel', { name: 'Request Parameters' })
  expect(panel.querySelector('pre')?.textContent).toBe(
    JSON.stringify(params, null, 2)
  )
  expect(
    within(panel).getByRole('button', { name: 'Copy request parameters' })
  ).toBeVisible()
  expect(
    screen.queryByRole('region', { name: 'Preview' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Overview' }))
  expect(
    screen.getByRole('region', { name: 'Preview' }).querySelector('video')
  ).toHaveAttribute('src', mediaUrl)
})

test('historical tasks without a snapshot explain missing parameters instead of guessing them', async () => {
  renderDetails({
    ...task,
    data: { prompt: 'untrusted response is not the request' },
  })
  await userEvent.click(screen.getByRole('tab', { name: 'Request Parameters' }))
  const panel = screen.getByRole('tabpanel', { name: 'Request Parameters' })
  expect(
    within(panel).getByText('No saved request parameters for this task.')
  ).toBeVisible()
  expect(panel).not.toHaveTextContent('untrusted response')
  expect(
    within(panel).queryByRole('button', { name: 'Copy request parameters' })
  ).not.toBeInTheDocument()
})

test('task row parameters action opens the saved request directly and details remains available', async () => {
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <TaskDetailsCell
          log={{
            ...task,
            status: 'FAILURE',
            request_params: { prompt: 'original input', seconds: 8 },
          }}
          isAdmin={false}
          isRoot={false}
        />
      </QueryClientProvider>
    </I18nextProvider>
  )
  expect(screen.getByRole('button', { name: 'View details' })).toBeVisible()
  await userEvent.click(
    screen.getByRole('button', { name: 'Request Parameters' })
  )
  expect(
    screen.getByRole('tab', { name: 'Request Parameters' })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('tabpanel', { name: 'Request Parameters' })
  ).toHaveTextContent('original input')
})
