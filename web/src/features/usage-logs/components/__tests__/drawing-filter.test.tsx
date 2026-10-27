import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { fetchLogsByCategory } from '../../lib/utils'
import { TaskLogsFilterBar } from '../task-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const originalAuth = useAuthStore.getState().auth
let client: QueryClient

afterEach(() => {
  client?.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
})

function FilterFixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <UsageLogsProvider>
      <TaskLogsFilterBar table={table} logCategory='drawing' />
    </UsageLogsProvider>
  )
}

test.each([false, true])(
  'drawing request-ID filter keeps the MJ list contract and admin=%s permissions',
  async (isAdmin) => {
    useAuthStore.getState().auth.setUser({
      id: 2,
      username: 'viewer',
      role: isAdmin ? ROLE.ADMIN : ROLE.USER,
    })
    vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    const root = createRootRoute()
    const auth = createRoute({
      getParentRoute: () => root,
      id: '_authenticated',
    })
    const logs = createRoute({
      getParentRoute: () => auth,
      path: '/usage-logs/$section',
      component: FilterFixture,
      validateSearch: (search: Record<string, unknown>) => search,
    })
    const router = createRouter({
      routeTree: root.addChildren([auth.addChildren([logs])]),
      history: createMemoryHistory({
        initialEntries: ['/usage-logs/drawing?page=3'],
      }),
    })
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(
      'Filter by task or request ID'
    )
    expect(screen.queryByPlaceholderText('Channel ID') !== null).toBe(isAdmin)
    await user.type(input, 'request-42{Enter}')
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({
        page: 1,
        filter: 'request-42',
      })
    )
    await fetchLogsByCategory({
      logCategory: 'drawing',
      isAdmin,
      page: 1,
      pageSize: 20,
      searchParams: router.state.location.search,
      columnFilters: [],
    })
    expect(get).toHaveBeenCalledOnce()
    const requestedUrl = new URL(get.mock.calls[0][0], window.location.origin)
    expect(requestedUrl.pathname).toBe(isAdmin ? '/api/mj' : '/api/mj/self')
    expect(requestedUrl.searchParams.get('mj_id')).toBe('request-42')
    expect(requestedUrl.searchParams.get('p')).toBe('1')
    expect(requestedUrl.searchParams.has('request_id')).toBe(false)
  }
)
