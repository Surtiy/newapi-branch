import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { ApiDocs } from '../index'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn().mockResolvedValue({ data: { success: true, data: { base_url: '', docs: [{ id: 'video', title: 'Video API', category: 'Video', description: '', content: '## Create task\nPOST {{BASE_URL}}/v1/videos\n## Query task\nGET {{BASE_URL}}/v1/videos/id' }] } } }) } }))

afterEach(() => vi.restoreAllMocks())

it('scrolls to the current rendered heading when a table-of-contents item is clicked', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRootRoute({ component: ApiDocs })
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) })
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  const link = await screen.findByRole('button', { name: 'Query task' })
  const heading = screen.getByRole('heading', { name: 'Query task' })
  const scroll = vi.fn()
  Object.defineProperty(heading, 'scrollIntoView', { value: scroll, configurable: true })
  fireEvent.click(link)
  await waitFor(() => expect(scroll).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' }))
  client.clear()
})
