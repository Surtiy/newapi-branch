import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test } from 'vitest'

import en from '@/i18n/locales/en.json'

import type { TaskArtifactProjection, TaskLog } from '../../types'
import { TaskDetailsDialog } from '../dialogs/task-details-dialog'
import { TaskArtifactsCell } from '../task-artifacts'

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
